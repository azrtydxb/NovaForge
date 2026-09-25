package agents_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/redis/go-redis/v9"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real Redis socket is paused after handshake, rather than replacing Publish
// with a successful mock. go-redis must impose the command's actual deadline.
func TestAuditStalledRedisDoesNotBlockCompletion(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	id, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "work.get", ArgsJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	var mu sync.Mutex
	var sockets []net.Conn
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range sockets {
			c.Close()
		}
	}()
	go func() {
		for {
			c, e := lis.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			sockets = append(sockets, c)
			mu.Unlock()
		}
	}()
	opts := &redis.Options{Addr: lis.Addr().String(), MaxRetries: -1}
	rdb := agents.NewAuditRedisClient(opts)
	defer rdb.Close()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		c, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		done <- a.MaintainAudit(c, rdb)
	}()
	time.Sleep(50 * time.Millisecond)
	c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = a.Complete(c, id, "ok", "")
	cancel()
	if err != nil {
		t.Errorf("publisher blocked independent audit completion: %v", err)
	}
	select {
	case <-done:
		if time.Since(start) > 600*time.Millisecond {
			t.Error("Redis deadline exceeded")
		}
	case <-time.After(650 * time.Millisecond):
		t.Fatal("maintenance ignored deadline")
	}
}

type delayCommands struct{ delay time.Duration }

func (d delayCommands) DialHook(next redis.DialHook) redis.DialHook { return next }
func (d delayCommands) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xadd" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.delay):
			}
		}
		return next(ctx, cmd)
	}
}
func (d delayCommands) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func TestAuditPublisherRetainsPrefixProgress(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	rdb := agentsRedis(t)
	rdb.AddHook(delayCommands{30 * time.Millisecond})
	for i := 0; i < 12; i++ {
		if _, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: fmt.Sprintf("test.%d", i), ArgsJSON: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	remaining := 12
	for i := 0; i < 12 && remaining > 0; i++ {
		c, cancel := context.WithTimeout(ctx, 160*time.Millisecond)
		_ = a.MaintainAudit(c, rdb)
		cancel()
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM agents.tool_events WHERE run_id=$1 AND NOT published`, run.ID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
	}
	if remaining != 0 {
		t.Fatalf("short sweeps never drained prefix: %d pending", remaining)
	}
}

func TestAuditStalledXAddSocketDeadline(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	realRedis := agentsRedis(t)
	id, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "work.get", ArgsJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	forwarded := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	var once sync.Once
	var mu sync.Mutex
	var sockets []net.Conn
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range sockets {
			c.Close()
		}
	}()
	go func() {
		for {
			client, e := lis.Accept()
			if e != nil {
				return
			}
			upstream, e := net.Dial("tcp", realRedis.Options().Addr)
			if e != nil {
				client.Close()
				return
			}
			mu.Lock()
			sockets = append(sockets, client, upstream)
			mu.Unlock()
			var stalled atomic.Bool
			go func() {
				b := make([]byte, 65536)
				for {
					n, e := client.Read(b)
					if e != nil {
						return
					}
					isXadd := bytes.Contains(bytes.ToLower(b[:n]), []byte("xadd"))
					if isXadd {
						stalled.Store(true)
					}
					if _, e = upstream.Write(b[:n]); e != nil {
						return
					}
					if isXadd {
						once.Do(func() { close(forwarded) })
					}
				}
			}()
			go func() {
				b := make([]byte, 65536)
				for {
					n, e := upstream.Read(b)
					if e != nil {
						return
					}
					if stalled.Load() {
						<-stop
						return
					}
					if _, e = client.Write(b[:n]); e != nil {
						return
					}
				}
			}()
		}
	}()
	options := *realRedis.Options()
	options.Addr = lis.Addr().String()
	rdb := agents.NewAuditRedisClient(&options)
	defer rdb.Close()
	done := make(chan error, 1)
	go func() {
		c, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		done <- a.MaintainAudit(c, rdb)
	}()
	select {
	case <-forwarded:
	case <-time.After(2 * time.Second):
		t.Fatal("no actual XADD forwarded")
	}
	start := time.Now()
	c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = a.Complete(c, id, "ok", "")
	cancel()
	if err != nil {
		t.Fatalf("stalled XADD blocked audit: %v", err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("lost Redis response acknowledged")
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("XADD socket ignored deadline")
	}
	if time.Since(start) > 600*time.Millisecond {
		t.Fatal("publication exceeded bound")
	}
	var published bool
	if err = s.Pool().QueryRow(ctx, `SELECT published FROM agents.tool_events WHERE call_id=$1 AND outcome='pending'`, id).Scan(&published); err != nil || published {
		t.Fatalf("ambiguous XADD marked acknowledged: %v %v", published, err)
	}
}
