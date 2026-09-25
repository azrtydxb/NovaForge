package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestGateAnalysisImageConfiguration(t *testing.T) {
	for _, image := range []string{"", "registry.example/analysis@sha256:" + strings.Repeat("a", 64)} {
		t.Setenv("NF_GATE_ANALYSIS_IMAGE", image)
		field := reflect.ValueOf(LoadConfig()).FieldByName("GateAnalysisImage")
		if !field.IsValid() || field.Kind() != reflect.String || field.String() != image {
			t.Fatalf("operator analysis image was not loaded")
		}
	}
}
