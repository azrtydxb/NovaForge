package main

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestAnalysisSandboxStartup(t *testing.T) {
	image := "registry.example/analysis@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, image string
		config      *rest.Config
		loadErr     error
		wantErr     bool
	}{
		{name: "unconfigured"},
		{name: "configured", image: image, config: &rest.Config{Host: "https://127.0.0.1:6443"}},
		{name: "mutable-image", image: "registry.example/analysis:latest", config: &rest.Config{Host: "https://127.0.0.1:6443"}, wantErr: true},
		{name: "missing-kubernetes", image: image, loadErr: errors.New("no cluster credentials"), wantErr: true},
		{name: "nil-kubernetes", image: image, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded := false
			sandbox, err := loadAnalysisSandbox(tc.image, func() (*rest.Config, error) {
				loaded = true
				return tc.config, tc.loadErr
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("startup err=%v, want error=%v", err, tc.wantErr)
			}
			if tc.image == "" {
				if loaded || sandbox != nil {
					t.Fatal("unconfigured feature acquired Kubernetes authority")
				}
			} else if !tc.wantErr && (!loaded || sandbox == nil) {
				t.Fatal("operator image did not configure an analysis sandbox")
			}
			if tc.wantErr && sandbox != nil {
				t.Fatal("invalid configuration returned an executable sandbox")
			}
		})
	}
}
