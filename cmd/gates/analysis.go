package main

import (
	"fmt"

	"github.com/novaforge/novaforge/internal/gates"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func loadAnalysisSandbox(image string, loadConfig func() (*rest.Config, error)) (*gates.AnalysisSandbox, error) {
	// Absence leaves executable gates unavailable; it must never select a
	// workstation executor or silently acquire ambient cluster credentials.
	if image == "" {
		return nil, nil
	}
	config, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("analysis Kubernetes configuration: %w", err)
	}
	if config == nil {
		return nil, fmt.Errorf("analysis Kubernetes configuration is missing")
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("analysis Kubernetes client: %w", err)
	}
	return gates.NewAnalysisSandbox(client, config, image)
}
