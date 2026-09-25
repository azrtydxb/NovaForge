package tools

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/novaforge/novaforge/internal/workspace"
)

// closingStdio joins transport exit, protocol errors and explicit closure into
// one teardown. No exec-completion callback waits for Close or performs I/O.
type closingStdio struct {
	io.ReadWriteCloser
	closing       func()
	mu            sync.Mutex
	normalClosing bool
	unexpected    bool
	notified      bool
	closeStarted  bool
	closeDone     chan struct{}
	err           error
}

func newClosingStdio(stream io.ReadWriteCloser, closing func()) (*closingStdio, error) {
	s := &closingStdio{ReadWriteCloser: stream, closing: closing, closeDone: make(chan struct{})}
	lifecycle, ok := stream.(workspace.StdioLifecycle)
	if !ok {
		return s, fmt.Errorf("stdio lifecycle unavailable")
	}
	// Registration precedes initialize and replays an exit that won the race
	// with OpenStdio returning. No subsequent read is needed to cancel the run.
	lifecycle.OnExit(func() { s.beginClose(false) })
	return s, nil
}

func (s *closingStdio) prepareNormalClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unexpected {
		s.normalClosing = true
	}
}

func (s *closingStdio) beginClose(normal bool) {
	s.mu.Lock()
	if normal && !s.unexpected {
		s.normalClosing = true
	}
	if !normal && !s.normalClosing {
		s.unexpected = true
	}
	notify := s.unexpected && !s.notified
	if notify {
		s.notified = true
	}
	start := !s.closeStarted
	if start {
		s.closeStarted = true
	}
	s.mu.Unlock()
	if notify && s.closing != nil {
		s.closing()
	}
	if start {
		go func() {
			var err error
			if s.ReadWriteCloser == nil {
				err = fmt.Errorf("stdio stream unavailable")
			} else {
				err = s.ReadWriteCloser.Close()
			}
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
			close(s.closeDone)
		}()
	}
}

func (s *closingStdio) Close() error {
	s.beginClose(false)
	<-s.closeDone
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *closingStdio) closeForCompletion() error {
	s.beginClose(true)
	<-s.closeDone
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unexpected {
		return errors.Join(ErrMCPStdioClosed, s.err)
	}
	return s.err
}
func (s *closingStdio) Read(b []byte) (int, error) {
	n, err := s.ReadWriteCloser.Read(b)
	if err != nil {
		s.beginClose(false)
	}
	return n, err
}
func (s *closingStdio) Write(b []byte) (int, error) {
	n, err := s.ReadWriteCloser.Write(b)
	if err != nil {
		s.beginClose(false)
	}
	return n, err
}
