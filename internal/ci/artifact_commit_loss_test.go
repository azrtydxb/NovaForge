package ci_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/ci"
)

// This wraps a real PostgreSQL connection, dropping only the server's COMMIT
// acknowledgment after receipt proves it committed. No database is simulated.
type commitLossConn struct {
	net.Conn
	reader  *bufio.Reader
	pending []byte
	armed   *atomic.Bool
}

func (c *commitLossConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		header := make([]byte, 5)
		if _, err := io.ReadFull(c.reader, header); err != nil {
			return 0, err
		}
		size := int(binary.BigEndian.Uint32(header[1:])) - 4
		if size < 0 || size > 64<<20 {
			return 0, io.ErrUnexpectedEOF
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(c.reader, body); err != nil {
			return 0, err
		}
		if header[0] == 'C' && bytes.Equal(body, []byte("COMMIT\x00")) && c.armed.CompareAndSwap(true, false) {
			_ = c.Conn.Close()
			return 0, io.ErrUnexpectedEOF
		}
		c.pending = append(header, body...)
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func TestArtifactCommitResponseLossPreservesAcceptedBytes(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "commit-loss", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	blobs := artifactsBlobstore(t)
	cfg := s.pool.Config()
	cfg.ConnConfig.TLSConfig = nil
	cfg.ConnConfig.Fallbacks = nil
	dial := cfg.ConnConfig.DialFunc
	armed := new(atomic.Bool)
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		raw, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &commitLossConn{Conn: raw, reader: bufio.NewReader(raw), armed: armed}, nil
	}
	faultPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer faultPool.Close()
	faulty := ci.NewArtifactStore(faultPool, blobs)
	armed.Store(true)
	if _, err := faulty.Upload(ctx, job, "report", strings.NewReader("accepted bytes"), 14, connection); err == nil {
		t.Fatal("commit response was not lost")
	}
	if armed.Load() {
		t.Fatal("fault schedule did not intercept committed acknowledgment")
	}
	normal := ci.NewArtifactStore(s.pool, blobs)
	replay, err := normal.Upload(ctx, job, "report", strings.NewReader("accepted bytes"), 14, connection)
	if err != nil {
		t.Fatal(err)
	}
	object, err := normal.Open(ctx, replay.ID)
	if err != nil {
		t.Fatalf("successful replay refers to missing accepted evidence: %v", err)
	}
	defer object.Close()
	body, err := io.ReadAll(object)
	if err != nil || string(body) != "accepted bytes" {
		t.Fatalf("accepted bytes changed: %q %v", body, err)
	}
}
