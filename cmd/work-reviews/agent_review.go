package main

import (
	"context"
	"fmt"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"os"
)

func startIndependentReviews(ctx context.Context, cfg service.Config, server *reviews.GRPCServer) (func(), error) {
	if cfg.ReviewConfigFile == "" {
		return func() {}, nil
	}
	if server.Git == nil || cfg.AgentsAddr == "" || cfg.HMACSecret == "" {
		return nil, fmt.Errorf("configured independent review requires Git, agents and service authentication")
	}
	raw, err := os.ReadFile(cfg.ReviewConfigFile)
	if err != nil {
		return nil, err
	}
	limits, err := reviews.ParseReviewConfig(raw)
	if err != nil {
		return nil, err
	}
	executionGateway, err := reviews.NewExecutionGateway(ctx, server.Store, limits.ExecutionOwnerURL, cfg.AIAPIKey, cfg.AIModel)
	if err != nil {
		return nil, err
	}
	options, err := agentrun.ParseProviderOptions(cfg.AIProviderOptions)
	if err != nil {
		return nil, err
	}
	prices, err := agents.ParseModelPrices(cfg.AIModelPrices)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(cfg.AgentsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	worker := &reviews.ReviewWorker{Store: server.Store, Git: server.Git, Agents: agentsv1.NewAgentServiceClient(conn), Gateway: executionGateway, ProviderOptions: options, Config: limits, HMACSecret: cfg.HMACSecret}
	if price, ok := prices[cfg.AIModel]; ok {
		worker.Price = &price
	}
	server.ReviewWorker = worker
	workctx, cancel := context.WithCancel(ctx)
	go worker.Run(workctx)
	return func() { cancel(); conn.Close() }, nil
}
