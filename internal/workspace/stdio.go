package workspace

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/tools/remotecommand"
)

// StdioLifecycle exposes exec termination independently of another protocol
// read. OnExit installs one subscriber and synchronously replays an earlier
// exit. The callback may invalidate/cancel only: it must not block or do I/O.
// Notification is not proof that the workspace's other processes terminated.
type StdioLifecycle interface {
	io.ReadWriteCloser
	OnExit(func())
}

// OpenStdio starts a command only inside this run's workspace. The caller owns
// the returned pipes and must close them when the run ends. No subprocess is
// ever launched on agent-runtime's host; Kubernetes exec reaches the existing
// deny-egress pod with no service-account credential.
func (p *Provisioner) OpenStdio(ctx context.Context, runID uuid.UUID, command []string) (io.ReadWriteCloser, error) {
	if p.recordCleanup == nil {
		return nil, fmt.Errorf("durable workspace cleanup recorder unavailable")
	}
	identity, err := p.stdioIdentity(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err = p.recordCleanup(ctx, identity); err != nil {
		return nil, err
	}
	executor, err := p.executor(runID, command, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stream := &execStream{reader: outR, writer: inW, cancel: cancel, cleanup: func() error {
		p.invalidated.Store(runID, true)
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer stop()
		return p.DestroyConfirmed(cleanupCtx, identity)
	}}
	go func() {
		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: inR, Stdout: outW, Stderr: io.Discard})
		p.invalidated.Store(runID, true)
		stream.notifyExit()
		_ = inR.CloseWithError(err)
		_ = outW.CloseWithError(err)
		cancel()
	}()
	return stream, nil
}

type execStream struct {
	exitMu   sync.Mutex
	exited   bool
	onExit   func()
	reader   *io.PipeReader
	writer   *io.PipeWriter
	cancel   context.CancelFunc
	once     sync.Once
	cleanup  func() error
	closeErr error
}

func (s *execStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *execStream) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *execStream) Close() error {
	s.once.Do(func() { s.cancel(); _ = s.reader.Close(); _ = s.writer.Close(); s.closeErr = s.cleanup() })
	return s.closeErr
}

func (s *execStream) OnExit(fn func()) {
	s.exitMu.Lock()
	s.onExit = fn
	exited := s.exited
	s.exitMu.Unlock()
	if exited && fn != nil {
		fn()
	}
}
func (s *execStream) notifyExit() {
	s.exitMu.Lock()
	if s.exited {
		s.exitMu.Unlock()
		return
	}
	s.exited = true
	fn := s.onExit
	s.exitMu.Unlock()
	if fn != nil {
		fn()
	}
}
