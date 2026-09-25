package indexing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/redis/go-redis/v9"
)

// DeploymentConsumer consumes only the trusted delivery outbox stream. Redis
// publication must be restricted by platform credentials/ACLs; the payload is
// not a signed public API.
// Graph authority is independently restricted to this verified platform worker,
// which re-enters one organization before any owning-store operation.
type DeploymentConsumer struct {
	Store      *graph.Store
	RDB        *redis.Client
	HMACSecret string
	// Stream is overridden only for an isolated acceptance stream.
	Stream string
}

func (c *DeploymentConsumer) Handle(ctx context.Context, data []byte) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != graph.DeploymentEventWorker {
		return fmt.Errorf("deployment consumption requires named platform worker")
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("deployment event exceeds 64KiB")
	}
	var e graph.DeploymentSuccessEvent
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&e); err != nil {
		return fmt.Errorf("decode deployment event: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing deployment event")
	}
	if err = e.Validate(); err != nil {
		return err
	}
	token, err := svcauth.Mint(c.HMACSecret, graph.DeploymentEventWorker, e.OrgID, time.Minute)
	if err != nil {
		return err
	}
	org, err := svcauth.ScopeFromToken(c.HMACSecret, token)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(authz.WithScope(ctx, org), 20*time.Second)
	defer cancel()
	if c.Store == nil {
		return fmt.Errorf("deployment graph store unavailable")
	}
	err = c.Store.RecordDeploymentSuccess(call, e)
	if errors.Is(err, graph.ErrIndexDeleted) {
		return nil
	} // permanent tombstone: acknowledge without resurrection
	return err
}

// Run uses a stable consumer identity across replicas/restarts so the existing
// stream helper redelivers unacked messages. Database fences serialize projection.
func (c *DeploymentConsumer) Run(ctx context.Context) error {
	if c.Store == nil || c.RDB == nil {
		return fmt.Errorf("deployment consumer dependencies required")
	}
	stream := c.Stream
	if stream == "" {
		stream = graph.StreamDeploymentSucceeded
	}
	for ctx.Err() == nil {
		token, err := svcauth.MintPlatform(c.HMACSecret, graph.DeploymentEventWorker, time.Minute)
		if err != nil {
			return err
		}
		name, err := svcauth.VerifyPlatform(c.HMACSecret, token)
		if err != nil {
			return err
		}
		worker := authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: name})
		err = events.Consume(worker, c.RDB, stream, graph.DeploymentEventWorker, graph.DeploymentEventWorker, c.Handle)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("deployment graph consumer stopped; retrying: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	return nil
}
