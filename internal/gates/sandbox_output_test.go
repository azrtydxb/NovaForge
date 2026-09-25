package gates

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

type sandboxStreamFunc func(context.Context, remotecommand.StreamOptions) error

func (f sandboxStreamFunc) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	return f(ctx, opts)
}

func TestSandboxCopyCannotBypassOutputBound(t *testing.T) {
	var dst sandboxBuffer
	// SPDY's reader has no WriterTo. io.Copy must not discover an unbounded
	// promoted ReaderFrom on the destination instead of calling bounded Write.
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", sandboxOutputLimit+1))}
	_, err := io.Copy(&dst, source)
	if err == nil {
		t.Fatal("io.Copy bypassed analysis output limit")
	}
	if dst.Len() > sandboxOutputLimit {
		t.Fatalf("retained %d bytes beyond bound", dst.Len())
	}
}

func TestSandboxRejectsOverflowDespiteSuccessfulRemoteStatus(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		for _, nonzero := range []bool{false, true} {
			name := "stdout"
			if stderr {
				name = "stderr"
			}
			if nonzero {
				name += "-remote-error"
			}
			t.Run(name, func(t *testing.T) {
				stream := sandboxStreamFunc(func(_ context.Context, opts remotecommand.StreamOptions) error {
					dst := opts.Stdout
					if stderr {
						dst = opts.Stderr
					}
					// client-go logs copy errors, drains the stream and returns remote status;
					// writer error alone therefore cannot prevent truncated-evidence success.
					_, _ = io.Copy(dst, struct{ io.Reader }{strings.NewReader(strings.Repeat("x", sandboxOutputLimit+1))})
					if nonzero {
						return utilexec.CodeExitError{Err: errors.New("remote exit"), Code: 2}
					}
					return nil
				})
				out, _, err := collectSandboxOutput(context.Background(), stream, nil)
				if err == nil || !strings.Contains(err.Error(), "output exceeds") {
					t.Fatalf("overflow became evidence: len=%d err=%v", len(out), err)
				}
				if len(out) != 0 {
					t.Fatal("overflow returned partial evidence")
				}
			})
		}
	}
}

// SPDY returns on context cancellation without joining its output-copy
// goroutines. Even reading the overflow flag then races with a final chunk.
func TestSandboxCancellationWithInFlightOutput(t *testing.T) {
	payload := make([]byte, sandboxOutputLimit+1)
	for _, streamError := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		t.Run(streamError.Error(), func(t *testing.T) {
			done := make(chan struct{})
			stream := sandboxStreamFunc(func(_ context.Context, opts remotecommand.StreamOptions) error {
				go func() {
					defer close(done)
					_, _ = opts.Stdout.Write(payload)
					_, _ = opts.Stderr.Write(payload)
				}()
				return streamError
			})
			out, _, err := collectSandboxOutput(context.Background(), stream, nil)
			<-done
			if !errors.Is(err, streamError) || len(out) != 0 {
				t.Fatalf("transport failure became evidence: len=%d err=%v", len(out), err)
			}
		})
	}
}

func TestSandboxAcceptsExactOutputBound(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), sandboxOutputLimit)
	stream := sandboxStreamFunc(func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, err := io.Copy(opts.Stdout, struct{ io.Reader }{bytes.NewReader(payload)})
		return err
	})
	out, code, err := collectSandboxOutput(context.Background(), stream, nil)
	if err != nil || code != 0 || !bytes.Equal(out, payload) {
		t.Fatalf("exact bound rejected: len=%d code=%d err=%v", len(out), code, err)
	}
}
