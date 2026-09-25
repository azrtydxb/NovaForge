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

// TestAuditPublisherRetainsPrefixProgress: a sweep cancelled part-way through
// publishing keeps the prefix it already published and acked, so repeated short
// sweeps drain the backlog instead of restarting it.
//
// The per-sweep budget is measured rather than written down. It used to be a
// constant 160ms, calibrated against a datastore on the same machine. Against
// the cluster's datastores, reached over a VPN, acquiring a connection, taking
// the publisher lock and reading the backlog costs more than that on its own,
// so no sweep ever reached its first publish: the backlog stayed at its full
// size and the test failed without once exercising the behaviour it asserts.
// A budget derived from one measured sweep holds on both.
func TestAuditPublisherRetainsPrefixProgress(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	a := agents.NewAuditLog(s.Pool())
	rdb := agentsRedis(t)
	rdb.AddHook(delayCommands{30 * time.Millisecond})

	const events = 12
	record := func(run uuid.UUID) {
		t.Helper()
		for i := 0; i < events; i++ {
			if _, err := a.Record(ctx, agents.Entry{RunID: run, Tool: fmt.Sprintf("test.%d", i), ArgsJSON: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	pending := func(run uuid.UUID) int {
		t.Helper()
		var n int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM agents.tool_events WHERE run_id=$1 AND NOT published`, run).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// One unhindered sweep gives the cost of publishing and acking one event
	// against this deployment, and warms the pool so the measurement is not
	// dominated by opening the first connection.
	warm := mustCreateRun(t, s, ctx, org)
	record(warm.ID)
	began := time.Now()
	if err := a.MaintainAudit(ctx, rdb); err != nil {
		t.Fatal(err)
	}
	if n := pending(warm.ID); n != 0 {
		t.Fatalf("an uninterrupted sweep left %d of %d events unpublished", n, events)
	}
	perEvent := time.Since(began) / events

	// Each sweep gets room for a couple of events and never for all of them, so
	// every sweep but the last is cancelled with work outstanding.
	run := mustCreateRun(t, s, ctx, org)
	record(run.ID)
	budget := 2 * perEvent
	remaining := events
	for i := 0; i < 4*events && remaining > 0; i++ {
		c, cancel := context.WithTimeout(ctx, budget)
		_ = a.MaintainAudit(c, rdb)
		cancel()
		n := pending(run.ID)
		if n > remaining {
			t.Fatalf("sweep %d raised the backlog from %d to %d", i, remaining, n)
		}
		remaining = n
	}
	if remaining != 0 {
		t.Fatalf("short sweeps of %v never drained the backlog: %d of %d pending", budget, remaining, events)
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
