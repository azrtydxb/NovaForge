package tools

import (
	"errors"
	"github.com/novaforge/novaforge/internal/mcp"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type observedExitTransport struct {
	mu       sync.Mutex
	ended    bool
	callback func()
	closes   atomic.Int32
	closeErr error
	onClose  func()
}

func (s *observedExitTransport) OnExit(fn func()) {
	s.mu.Lock()
	s.callback = fn
	ended := s.ended
	s.mu.Unlock()
	if ended {
		fn()
	}
}
func (s *observedExitTransport) exit() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	fn := s.callback
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
func (s *observedExitTransport) Close() error {
	s.closes.Add(1)
	if s.onClose != nil {
		s.onClose()
	}
	s.exit()
	return s.closeErr
}
func (s *observedExitTransport) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *observedExitTransport) Write(b []byte) (int, error) { return len(b), nil }

func TestStdioExitBeforeRegistrationAndCrossRunIsolation(t *testing.T) {
	first, second := &observedExitTransport{}, &observedExitTransport{}
	first.exit()
	var cancelledFirst, cancelledSecond atomic.Int32
	a, err := newClosingStdio(first, func() { cancelledFirst.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	b, err := newClosingStdio(second, func() { cancelledSecond.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if cancelledFirst.Load() != 1 || cancelledSecond.Load() != 0 {
		t.Fatal("exit replay lost or crossed run boundary")
	}
	if err = a.closeForCompletion(); !errors.Is(err, ErrMCPStdioClosed) {
		t.Fatal("unexpected exit relabelled normal", err)
	}
	if err = b.closeForCompletion(); err != nil {
		t.Fatal(err)
	}
	if first.closes.Load() != 1 || second.closes.Load() != 1 || cancelledSecond.Load() != 0 {
		t.Fatal("normal close cancelled another run or repeated teardown")
	}
}
func TestStdioNormalCloseAndExitDoNotDeadlock(t *testing.T) {
	stream := &observedExitTransport{}
	var cancelled atomic.Int32
	session, err := newClosingStdio(stream, func() { cancelled.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	// Close synchronously invokes the transport callback. If that callback
	// waits on Close/once/another lock, this deterministic case deadlocks.
	if err = session.closeForCompletion(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := session.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if cancelled.Load() != 0 || stream.closes.Load() != 1 {
		t.Fatalf("normal terminal closure became abnormal: cancel=%d closes=%d", cancelled.Load(), stream.closes.Load())
	}
}
func TestStdioUnexpectedExitRetainsTeardownFailure(t *testing.T) {
	failure := errors.New("teardown unavailable")
	stream := &observedExitTransport{closeErr: failure}
	var cancelled atomic.Bool
	session, err := newClosingStdio(stream, func() { cancelled.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	stream.exit()
	if !cancelled.Load() {
		t.Fatal("exit did not synchronously cancel")
	}
	if err = session.closeForCompletion(); !errors.Is(err, failure) || !errors.Is(err, ErrMCPStdioClosed) {
		t.Fatalf("lost lifecycle failure: %v", err)
	}
	if stream.closes.Load() != 1 {
		t.Fatal("teardown not exactly once")
	}
}

func TestNormalFinalizationMarksAllWorkspaceSessionsBeforeTeardown(t *testing.T) {
	first, second := &observedExitTransport{}, &observedExitTransport{}
	first.onClose = second.exit // tearing down one workspace ends every exec in it
	var cancellations atomic.Int32
	a, err := newClosingStdio(first, func() { cancellations.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	b, err := newClosingStdio(second, func() { cancellations.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	ca := mcp.NewStdioClient(mcp.ServerDef{Name: "a"}, []string{"a"}, a)
	cb := mcp.NewStdioClient(mcp.ServerDef{Name: "b"}, []string{"b"}, b)
	offered := Offered{clients: []*mcp.Client{ca, cb}, stdio: map[*mcp.Client]*closingStdio{ca: a, cb: b}}
	if err = offered.CloseForCompletion(); err != nil {
		t.Fatalf("normal workspace teardown misclassified peer session: %v", err)
	}
	if cancellations.Load() != 0 {
		t.Fatal("normal finalization cancelled run")
	}
}
