package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/service"
)

func TestConfigReadsEnv(t *testing.T) {
	t.Setenv("SERVICE_NAME", "identity")
	t.Setenv("GRPC_PORT", "9091")
	t.Setenv("DB_SCHEMA", "identity")
	t.Setenv("DATABASE_URL", "postgres://x/y")
	cfg := service.LoadConfig()
	if cfg.Name != "identity" || cfg.GRPCPort != 9091 || cfg.DBSchema != "identity" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestConfigDefaults(t *testing.T) {
	os.Clearenv()
	cfg := service.LoadConfig()
	if cfg.HealthPort != 8090 {
		t.Fatalf("want default health port 8090, got %d", cfg.HealthPort)
	}
}

func TestHealthzReadyWhenChecksPass(t *testing.T) {
	h := service.HealthHandler(func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHealthzUnreadyWhenCheckFails(t *testing.T) {
	h := service.HealthHandler(func(context.Context) error { return errors.New("db down") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Fatal("want the failure reason in the body")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Serve(ctx, service.Config{HealthPort: 0}, nil, func(context.Context) error { return nil })
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("want clean shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of cancellation")
	}
}
