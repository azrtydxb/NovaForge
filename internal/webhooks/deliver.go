package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
)

// ConsumerGroup is the Redis Streams consumer group the delivery worker reads
// under. It is exported so a test can inspect the group's pending list: "the
// worker gave up and acknowledged the message" and "the worker is still
// retrying" are indistinguishable from the outside otherwise.
const ConsumerGroup = "webhooks"

// EventPush is the event name a push is delivered under. Event names are part of
// the contract with whoever registered the hook — they are what a hook filters
// on — so they are constants here rather than strings spelled out at each use.
//
// It is the only event delivered today, and not because the others were
// forgotten: a hook belongs to a repository, so an event can only be routed to
// one if it says which repository it happened in. events.StreamGitPush does.
// Engineering Run and CI result events are not published on any stream at all
// yet (grep events.Publish: identity, agents, cleanup, deployment and gitops are
// the only publishers), and events.StreamRunsDeleted carries run ids with no
// repository. Adding either means a publisher in the service that owns it; this
// worker then needs one more stream and one more constant, and a hook already
// filters by name.
const EventPush = "push"

// defaultMaxAttempts bounds how many times one event is offered to one endpoint.
// An endpoint that is simply broken must not become permanent load: five attempts
// over a few seconds is enough to ride out a restart, and after that the delivery
// is left failed for whoever owns the endpoint to see.
const defaultMaxAttempts = 5

// defaultBackoff is the first wait between attempts; each subsequent wait
// doubles.
const defaultBackoff = 2 * time.Second

// defaultTimeout bounds one attempt. Without it a receiver that accepts the
// connection and never answers would hold a delivery open indefinitely, and the
// worker with it — one slow endpoint would stop every hook in the deployment.
const defaultTimeout = 10 * time.Second

// claimInterval is how often the worker reclaims messages a previous consumer
// took and never acknowledged, so a pod that died mid-delivery does not strand
// the event forever.
const claimInterval = 30 * time.Second

// minIdle is how long a delivered-but-unacked message must sit before it is
// reclaimed. It must comfortably exceed the time a full set of bounded attempts
// takes, or a replica would reclaim work another replica is still doing.
const minIdle = 2 * time.Minute

// Worker delivers domain events to the endpoints registered for the repository
// they happened in.
//
// It is a consumer group, not a plain read: git-platform runs with more than one
// replica, and a hook that fired once per replica would double every notification
// the platform sends.
//
// Note for whoever operates this: the chart confines git-platform's egress to the
// cluster (deploy/helm/novaforge/values.yaml lists it under
// networkPolicy.airGapped), so in the default deployment a hook can reach an
// in-cluster endpoint and an endpoint on the public internet is dropped by
// Cilium. That is a deliberate posture, not a defect here — but a hook whose
// deliveries all fail with a connection error in an otherwise healthy deployment
// is this, and the delivery history is where it shows.
type Worker struct {
	RDB   *redis.Client
	Store *Store

	// Client sends the deliveries. Nil gets one with defaultTimeout and
	// redirects refused.
	Client *http.Client

	// Consumer names this worker's identity in the group. Empty gets a random
	// one, which is correct for a single replica; a deployment with several
	// should give each pod its own stable name so a crashed pod's pending
	// messages can be reclaimed by name.
	Consumer string

	// PushStream overrides events.StreamGitPush, so a test consumes a stream of
	// its own rather than the one every other consumer on the cluster shares.
	PushStream string

	// MaxAttempts and Backoff bound the retries. Zero means the defaults.
	MaxAttempts int
	Backoff     time.Duration
}

func (w *Worker) pushStream() string {
	if w.PushStream != "" {
		return w.PushStream
	}
	return events.StreamGitPush
}

func (w *Worker) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{
		Timeout: defaultTimeout,
		// A redirect is not followed. The body is signed for the endpoint that
		// was registered, and following a redirect would send that signed body —
		// including everything the push carries — to a host nobody registered,
		// chosen by whoever controls the endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the endpoint redirected; a signed delivery is not forwarded to another address")
		},
	}
}

func (w *Worker) maxAttempts() int {
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return defaultMaxAttempts
}

func (w *Worker) backoff() time.Duration {
	if w.Backoff > 0 {
		return w.Backoff
	}
	return defaultBackoff
}

// Run consumes the push stream until ctx is cancelled, delivering each event to
// the hooks that asked for it.
//
// A message is acknowledged once its hooks have been attempted to the bound,
// whether or not they succeeded. Leaving it pending would have every restart of
// the service deliver the same push again to the same broken endpoint, forever:
// the durable record of the failure is the delivery history, not the stream.
func (w *Worker) Run(ctx context.Context) error {
	if w.RDB == nil || w.Store == nil {
		return errors.New("webhooks: the worker needs both Redis and a store")
	}
	stream := w.pushStream()
	if err := events.EnsureGroup(ctx, w.RDB, stream, ConsumerGroup); err != nil {
		return fmt.Errorf("ensure consumer group: %w", err)
	}
	consumer := w.Consumer
	if consumer == "" {
		consumer = "webhooks-" + uuid.NewString()
	}

	ticker := time.NewTicker(claimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.autoClaim(ctx, consumer)
			continue
		default:
		}

		res, err := w.RDB.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    ConsumerGroup,
			Consumer: consumer,
			Streams:  []string{stream, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("webhooks: XReadGroup: %v", err)
			continue
		}
		for _, s := range res {
			for _, msg := range s.Messages {
				w.handleAndAck(ctx, stream, msg)
			}
		}
	}
}

// autoClaim takes over messages idle for longer than minIdle and handles them as
// the main loop does, so a consumer that died mid-delivery does not strand its
// event.
func (w *Worker) autoClaim(ctx context.Context, consumer string) {
	stream := w.pushStream()
	start := "0"
	for {
		msgs, next, err := w.RDB.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: stream, Group: ConsumerGroup, Consumer: consumer,
			MinIdle: minIdle, Start: start, Count: 10,
		}).Result()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("webhooks: XAutoClaim: %v", err)
			}
			return
		}
		for _, msg := range msgs {
			w.handleAndAck(ctx, stream, msg)
		}
		if next == "0-0" || next == "" || len(msgs) == 0 {
			return
		}
		start = next
	}
}

func (w *Worker) handleAndAck(ctx context.Context, stream string, msg redis.XMessage) {
	if err := w.handle(ctx, msg); err != nil {
		// A message that could not even be understood is acknowledged too: it
		// will never become understandable, and leaving it pending would have
		// every pass trip over it instead of doing the deliveries behind it.
		log.Printf("webhooks: handle message %s: %v", msg.ID, err)
	}
	if err := w.RDB.XAck(ctx, stream, ConsumerGroup, msg.ID).Err(); err != nil && ctx.Err() == nil {
		log.Printf("webhooks: XAck %s: %v", msg.ID, err)
	}
}

// payload is the body of a delivery. It carries the event's own fields under a
// name, rather than being the event flattened: a receiver written against "push"
// must not break the day a second event type is delivered with different fields.
type payload struct {
	Event    string            `json:"event"`
	Delivery string            `json:"delivery"`
	OrgID    uuid.UUID         `json:"org_id"`
	RepoID   uuid.UUID         `json:"repo_id"`
	RepoName string            `json:"repository"`
	At       time.Time         `json:"at"`
	Push     *events.PushEvent `json:"push,omitempty"`
}

func (w *Worker) handle(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["data"].(string)
	if !ok {
		return fmt.Errorf("message %s carries no data field", msg.ID)
	}
	var evt events.PushEvent
	// Unknown fields are refused: a stream message that is not the event this
	// worker thinks it is would otherwise be delivered as a push with every
	// field zero, and a receiver cannot tell that from a real push.
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&evt); err != nil {
		return fmt.Errorf("decode push event: %w", err)
	}
	if evt.OrgID == uuid.Nil || evt.RepoID == uuid.Nil {
		// A push event with no repository id cannot be routed to a repository's
		// hooks. This was a real defect for the CI scheduler, which matched
		// nothing and silently scheduled no run; here it would be silence too.
		return fmt.Errorf("push event %s names no organization or repository", msg.ID)
	}

	// The organization is re-entered from the event, which git-platform published
	// itself — never from a client request. Every store read below then takes its
	// predicate from this scope, exactly as the indexer does for the same stream.
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: evt.OrgID, ActorKind: "service"})

	targets, err := w.Store.targets(ctx, evt.RepoID, EventPush)
	if err != nil {
		return fmt.Errorf("hooks for %s: %w", evt.RepoID, err)
	}
	if len(targets) == 0 {
		return nil
	}

	body, err := json.Marshal(payload{
		Event: EventPush, Delivery: msg.ID, OrgID: evt.OrgID, RepoID: evt.RepoID,
		RepoName: evt.RepoName, At: evt.At, Push: &evt,
	})
	if err != nil {
		return fmt.Errorf("marshal delivery body: %w", err)
	}

	for _, t := range targets {
		w.deliver(ctx, t, EventPush, msg.ID, body)
	}
	return nil
}

// deliver posts body to one endpoint, retrying to the bound and recording every
// attempt. The bytes are identical on every attempt — including the delivery id —
// so a receiver that already processed one can recognize the repeat, and so the
// signature it verified the first time still verifies.
func (w *Worker) deliver(ctx context.Context, t target, event, delivery string, body []byte) {
	max := w.maxAttempts()
	wait := w.backoff()
	for attempt := 1; attempt <= max; attempt++ {
		status, reqErr := w.post(ctx, t, event, delivery, body)
		record := Delivery{HookID: t.hook.ID, Event: event, StatusCode: status, Attempt: attempt}
		if reqErr != nil {
			record.Error = reqErr.Error()
		}
		if err := w.Store.recordDelivery(ctx, record); err != nil && ctx.Err() == nil {
			// The notification has already been sent. Failing the delivery here
			// would send it again, so the loss of the record is logged instead.
			log.Printf("webhooks: record delivery for hook %s: %v", t.hook.ID, err)
		}
		if record.Delivered() {
			return
		}
		if attempt == max || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// post makes one attempt. It returns the response status, or 0 and an error when
// there was no response at all.
func (w *Worker) post(ctx context.Context, t target, event, delivery string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.hook.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NovaForge-Hookshot")
	req.Header.Set("X-NovaForge-Event", event)
	// The delivery id is the stream message id: stable across retries, so a
	// receiver can deduplicate. At-least-once is the guarantee Redis Streams
	// gives, so a receiver needs something to deduplicate on.
	req.Header.Set("X-NovaForge-Delivery", delivery)
	if t.secret != "" {
		req.Header.Set("X-NovaForge-Signature", Sign(body, t.secret))
	}

	resp, err := w.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// The body is drained and discarded: it is not evidence of anything, and a
	// receiver that answers with megabytes should not be able to spend this
	// process's memory. Draining (rather than closing straight away) is what lets
	// the connection be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
	return resp.StatusCode, nil
}

// Sign is the signature a receiver verifies: HMAC-SHA256 of the exact bytes that
// were sent, under the hook's secret, hex-encoded.
//
// It signs the body and nothing else — not a re-marshalled copy, not the event
// before the envelope was added — because the receiver can only recompute it over
// what it actually read.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
