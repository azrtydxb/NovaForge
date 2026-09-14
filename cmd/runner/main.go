// Command runner is the NovaForge CI runner: it registers with the
// ci-runner service, then holds a single persistent outbound Connect
// stream that jobs are pushed down, so it never needs inbound network
// reachability. It executes each job it receives and streams live output
// back as log_chunk frames. The protocol itself is runner.Session.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := env("CI_ADDR", "localhost:9095")
	orgID := env("RUNNER_ORG_ID", "")
	name := env("RUNNER_NAME", hostname())
	labels := splitLabels(env("RUNNER_LABELS", "linux"))
	workdir := env("RUNNER_WORKDIR", filepath.Join(os.TempDir(), "novaforge-runner"))

	// podExec is the isolated execution path. It is nil only outside a
	// cluster, where the host fallback applies and must be explicitly enabled.
	podExec, err := runner.NewPodExecutorFromCluster(env("RUNNER_JOB_NAMESPACE", "novaforge"))
	if err != nil {
		log.Fatalf("runner: kubernetes client: %v", err)
	}
	switch {
	case podExec != nil:
		log.Printf("runner: jobs execute in pods in namespace %s", env("RUNNER_JOB_NAMESPACE", "novaforge"))
	case runner.LocalExecutionAllowed():
		log.Println("runner: WARNING jobs execute on this host (NOVAFORGE_ALLOW_LOCAL_EXEC=1)")
	default:
		log.Fatalln("runner: not running in a cluster and NOVAFORGE_ALLOW_LOCAL_EXEC is not set; " +
			"refusing to start rather than run repository-supplied commands on this host")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("runner: dial %s: %v", addr, err)
	}
	defer conn.Close()
	client := civ1.NewRunnerServiceClient(conn)

	regResp, err := client.Register(ctx, &civ1.RegisterRequest{OrgId: orgID, Name: name, Labels: labels})
	if err != nil {
		log.Fatalf("runner: register: %v", err)
	}
	runnerID := regResp.GetRunnerId()

	upload := runner.UploadArtifactsThrough(client, runnerID)
	var exec runner.Executor
	if podExec != nil {
		podExec.OnArtifacts = upload
		exec = podExec
	} else {
		exec = &runner.LocalExecutor{Workdir: workdir, OnArtifacts: upload}
	}
	log.Printf("runner: registered as %s (%s), labels %v", name, runnerID, labels)

	session := &runner.Session{Client: client, RunnerID: runnerID, Executor: exec}
	backoff := initialBackoff
	for ctx.Err() == nil {
		if err := session.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("runner: connection error: %v (retrying in %s)", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = initialBackoff
	}
}

func splitLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "runner"
	}
	return h
}
