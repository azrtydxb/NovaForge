package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/swarm"
	"github.com/novaforge/novaforge/internal/work"
)

// swarmTickInterval is how often every open epic is ticked.
const swarmTickInterval = 30 * time.Second

// runSwarmScheduler ticks every open epic, starting an Agent Run for each
// ready subtask whose role maps to an enabled agent.
//
// Without this the scheduler existed and was unit-tested and nothing ever
// called it, so a decomposed epic sat with its dependency-free subtasks
// open forever — from outside, indistinguishable from a platform that had
// decided not to start them. It is the same shape of defect as a CI job
// that is claimed by nobody.
//
// It lives in work-reviews because the scheduler reads and claims work
// items through work.Store, and this is the service that owns that schema.
// Starting a run is an RPC to agent-runtime rather than a second writer on
// the agents schema, so the one-schema-per-service boundary holds.
func runSwarmScheduler(ctx context.Context, workStore *work.Store, client agentsv1.AgentServiceClient, hmacSecret string) {
	ticker := time.NewTicker(swarmTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			epics, err := workStore.OpenEpics(ctx)
			if err != nil {
				log.Printf("work-reviews: swarm: list open epics: %v", err)
				continue
			}
			for _, e := range epics {
				tickEpic(ctx, workStore, client, hmacSecret, e)
			}
		}
	}
}

// tickEpic ticks one epic inside its own organization's scope. One epic's
// trouble is logged and skipped rather than stopping the loop, so a single
// misconfigured epic cannot stall every other organization's work.
func tickEpic(ctx context.Context, workStore *work.Store, client agentsv1.AgentServiceClient, hmacSecret string, e work.Epic) {
	// The scheduler acts as the platform, not as a member: it re-enters each
	// organization explicitly, reads nothing outside it, and presents a
	// service token naming only that organization on every outbound call.
	orgCtx := authz.WithScope(ctx, authz.Scope{OrgID: e.OrgID, ActorKind: "service"})
	callCtx, err := withServiceIdentity(orgCtx, hmacSecret, e.OrgID)
	if err != nil {
		log.Printf("work-reviews: swarm: %v", err)
		return
	}

	epic, err := workStore.Get(orgCtx, e.ID)
	if err != nil {
		log.Printf("work-reviews: swarm: read epic %s: %v", e.ID, err)
		return
	}
	// An agent run always has a human answerable for it. An epic with no
	// human assignee is left alone rather than started under nobody's name.
	if epic.AssigneeKind != "user" || epic.AssigneeID == uuid.Nil {
		return
	}

	listed, err := client.ListAgents(callCtx, &agentsv1.ListAgentsRequest{})
	if err != nil {
		log.Printf("work-reviews: swarm: list agents for org %s: %v", e.OrgID, err)
		return
	}
	byRole := make(map[string]agents.Agent, len(listed.GetAgents()))
	for _, a := range listed.GetAgents() {
		if !a.GetEnabled() {
			continue
		}
		id, perr := uuid.Parse(a.GetId())
		if perr != nil {
			continue
		}
		byRole[a.GetRole()] = agents.Agent{
			ID: id, OrgID: e.OrgID, Name: a.GetName(), Role: a.GetRole(),
			ModelRef: a.GetModelRef(), Enabled: true,
		}
	}
	if len(byRole) == 0 {
		return
	}

	sch := &swarm.Scheduler{
		Work:      workStore,
		RoleAgent: byRole,
		StartRun: func(ctx context.Context, subtask work.Item, agent agents.Agent) (agents.Run, error) {
			resp, err := client.StartRun(callCtx, &agentsv1.StartRunRequest{
				AgentId:     agent.ID.String(),
				RepoId:      subtask.RepoID.String(),
				WorkItemKey: subtask.Key,
				SponsorId:   epic.AssigneeID.String(),
			})
			if err != nil {
				return agents.Run{}, fmt.Errorf("start run for %s: %w", subtask.Key, err)
			}
			id, err := uuid.Parse(resp.GetRun().GetId())
			if err != nil {
				return agents.Run{}, fmt.Errorf("parse started run id: %w", err)
			}
			return agents.Run{ID: id, OrgID: e.OrgID, AgentID: agent.ID, WorkItemID: subtask.ID}, nil
		},
		RunOutcome: agentRunOutcome(client, callCtx),
	}

	started, err := sch.Tick(orgCtx, e.ID)
	if err != nil {
		log.Printf("work-reviews: swarm: tick epic %s: %v", e.ID, err)
		return
	}
	if started > 0 {
		log.Printf("work-reviews: swarm: started %d run(s) for epic %s", started, epic.Key)
	}
}

// agentRunOutcome answers Scheduler.RunOutcome from agent-runtime: the runs
// started against a subtask, read through ListRunsForWorkItem, since the run
// rows live in a schema this service may not read.
//
// Nothing used to ask. A subtask's run could fail, go over budget or be
// cancelled and the subtask stayed in_progress, its dependents waiting for a
// "done" that no one was working towards — silence, from outside.
//
// A subtask can have more than one run (a retry after a failure). It is taken
// as failed only when none of its runs is still going or has succeeded, so an
// old failure never blocks a subtask a later run is working on.
func agentRunOutcome(client agentsv1.AgentServiceClient, callCtx context.Context) func(context.Context, work.Item) (string, error) {
	return func(_ context.Context, subtask work.Item) (string, error) {
		resp, err := client.ListRunsForWorkItem(callCtx, &agentsv1.ListRunsForWorkItemRequest{WorkItemId: subtask.ID.String()})
		if err != nil {
			return "", fmt.Errorf("list runs for %s: %w", subtask.Key, err)
		}
		outcome := ""
		for _, r := range resp.GetRuns() {
			switch r.GetState() {
			case "queued", "running", "succeeded":
				return r.GetState(), nil
			default:
				if outcome == "" {
					outcome = r.GetState()
				}
			}
		}
		return outcome, nil
	}
}

// withServiceIdentity attaches this service's token for orgID to outgoing
// calls, so agent-runtime resolves the caller as the platform acting inside
// exactly one organization.
func withServiceIdentity(ctx context.Context, hmacSecret string, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(hmacSecret, "swarm-scheduler", orgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, fmt.Errorf("mint service token: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}
