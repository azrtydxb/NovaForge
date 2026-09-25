package secrets

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewConfiguredBroker loads only operator-mounted configuration. An absent path
// keeps static administration available but refuses dynamic issuance; malformed
// configured providers fail startup, never silently fall back to static secrets.
func NewConfiguredBroker(pool *pgxpool.Pool, kek []byte, filename string) (*Broker, error) {
	if filename == "" {
		return NewBroker(pool, kek), nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("open OpenBao configuration failed")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20+1))
	dec.DisallowUnknownFields()
	var cfg OpenBaoConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, errors.New("invalid OpenBao configuration")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid OpenBao configuration trailing data")
	}
	if len(cfg.Bindings) == 0 {
		return nil, errors.New("OpenBao configuration requires bindings")
	}
	return NewOpenBaoBroker(pool, kek, cfg)
}
