// Package service holds the bootstrap every NovaForge service binary shares:
// configuration from the environment, a readiness endpoint that reflects real
// dependency health, a gRPC server, and a drain on SIGTERM.
//
// This exists so the nine service mains differ only in what they register,
// rather than each carrying its own copy of the same lifecycle code.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
)

// Config is the environment every service is given by the Helm chart.
type Config struct {
	Name        string
	DBSchema    string
	DatabaseURL string
	RedisURL    string
	GRPCPort    int
	HTTPPort    int
	SSHPort     int
	HealthPort  int
	GitDataDir  string

	IdentityAddr string
	GitAddr      string
	WorkAddr     string
	CIAddr       string
	GatesAddr    string
	AgentsAddr   string
	GraphAddr    string

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string

	AIEndpoint string
	AIAPIKey   string
	// AIProviderOptions is a JSON object of extra wire parameters for the
	// model server, parsed by agentrun.ParseProviderOptions.
	AIProviderOptions string
	AIModel           string
	EmbedEndpoint     string
	EmbedModel        string

	// AutoMergeEnabled and AutoMergeMaxFiles configure reviews.AutoMergePolicy.
	// Auto-merge is off unless a deployment turns it on: a fresh installation
	// must never merge anything without an explicit opt-in.
	AutoMergeEnabled  bool
	AutoMergeMaxFiles int
	// MaintenanceIntervalHours is how often the maintenance scanners run.
	MaintenanceIntervalHours int

	JWTSecret  string
	HMACSecret string
	SecretsKEK string
}

// LoadConfig reads the service environment. Ports default to the values the
// chart sets, so a binary run without an environment still starts.
func LoadConfig() Config {
	return Config{
		Name:                     env("SERVICE_NAME", ""),
		DBSchema:                 env("DB_SCHEMA", ""),
		DatabaseURL:              env("DATABASE_URL", ""),
		RedisURL:                 env("REDIS_URL", ""),
		GRPCPort:                 envInt("GRPC_PORT", 0),
		HTTPPort:                 envInt("HTTP_PORT", 0),
		SSHPort:                  envInt("SSH_PORT", 0),
		HealthPort:               envInt("HEALTH_PORT", 8090),
		GitDataDir:               env("GIT_DATA_DIR", "/data/repos"),
		IdentityAddr:             env("IDENTITY_ADDR", ""),
		GitAddr:                  env("GIT_ADDR", ""),
		WorkAddr:                 env("WORK_ADDR", ""),
		CIAddr:                   env("CI_ADDR", ""),
		GatesAddr:                env("GATES_ADDR", ""),
		AgentsAddr:               env("AGENTS_ADDR", ""),
		GraphAddr:                env("GRAPH_ADDR", ""),
		S3Endpoint:               env("S3_ENDPOINT", ""),
		S3AccessKey:              env("S3_ACCESS_KEY", ""),
		S3SecretKey:              env("S3_SECRET_KEY", ""),
		AIEndpoint:               env("AI_ENDPOINT", ""),
		AIAPIKey:                 env("AI_API_KEY", ""),
		AIProviderOptions:        env("AI_PROVIDER_OPTIONS", ""),
		AIModel:                  env("AI_MODEL", ""),
		EmbedEndpoint:            env("EMBED_ENDPOINT", ""),
		EmbedModel:               env("EMBED_MODEL", ""),
		AutoMergeEnabled:         env("AUTO_MERGE_ENABLED", "") == "true",
		AutoMergeMaxFiles:        envInt("AUTO_MERGE_MAX_FILES_CHANGED", 0),
		MaintenanceIntervalHours: envInt("MAINTENANCE_INTERVAL_HOURS", 0),
		JWTSecret:                env("JWT_SECRET", ""),
		HMACSecret:               env("HMAC_SECRET", ""),
		SecretsKEK:               env("SECRETS_KEK", ""),
	}
}

// CheckFunc reports whether the service's dependencies are usable.
type CheckFunc func(context.Context) error

// HealthHandler reports 200 only when check returns nil. A service whose
// database or Redis is unreachable must read as unready, not as healthy.
func HealthHandler(check CheckFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if check != nil {
			if err := check(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, "unready: %v\n", err)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
}

// Serve runs the health endpoint and, when grpcSrv is non-nil, the gRPC server,
// until ctx is cancelled or a termination signal arrives. It then drains for up
// to 30 seconds. Passing HealthPort 0 binds an arbitrary free port, which is
// what tests want.
func Serve(ctx context.Context, cfg Config, grpcSrv *grpc.Server, check CheckFunc) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/healthz", HealthHandler(check))
	healthSrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	hl, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.HealthPort))
	if err != nil {
		return fmt.Errorf("listen health: %w", err)
	}

	errCh := make(chan error, 2)
	go func() {
		if err := healthSrv.Serve(hl); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("health server: %w", err)
		}
	}()

	var gl net.Listener
	if grpcSrv != nil && cfg.GRPCPort > 0 {
		gl, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
		if err != nil {
			return fmt.Errorf("listen grpc: %w", err)
		}
		go func() {
			log.Printf("%s: grpc listening on :%d", cfg.Name, cfg.GRPCPort)
			if err := grpcSrv.Serve(gl); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				errCh <- fmt.Errorf("grpc server: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if grpcSrv != nil {
		done := make(chan struct{})
		go func() { grpcSrv.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-shutCtx.Done():
			grpcSrv.Stop()
		}
	}
	if err := healthSrv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("health shutdown: %w", err)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}
