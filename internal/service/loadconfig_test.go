package service_test

import (
	"reflect"
	"testing"

	"github.com/novaforge/novaforge/internal/service"
)

// TestLoadConfigPopulatesEveryField pins a defect that is invisible by
// inspection: a field can be added to Config, rendered into the chart, set
// in the pod's environment, and never read, because the literal in
// LoadConfig was not updated to match. The service then reports the feature
// as unconfigured while its operator can see the variable is set.
//
// Rather than listing the variables (a list that would drift the same way),
// this sets every string field to a marker and every int field to a
// distinct number through the environment, then asserts nothing came back
// zero.
func TestLoadConfigPopulatesEveryField(t *testing.T) {
	env := map[string]string{
		"SERVICE_NAME": "svc", "DB_SCHEMA": "s", "DATABASE_URL": "postgres://x",
		"REDIS_URL": "redis://x", "GRPC_PORT": "1", "HTTP_PORT": "2", "SSH_PORT": "3",
		"HEALTH_PORT": "4", "GIT_DATA_DIR": "/d", "IDENTITY_ADDR": "a:1",
		"GIT_ADDR": "a:2", "WORK_ADDR": "a:3", "CI_ADDR": "a:4", "GATES_ADDR": "a:5",
		"AGENTS_ADDR": "a:6", "GRAPH_ADDR": "a:7", "MCP_ADDR": "a:9", "S3_ENDPOINT": "a:8",
		"S3_ACCESS_KEY": "k", "S3_SECRET_KEY": "s", "AI_ENDPOINT": "http://m/v1",
		"AI_API_KEY": "sk-x", "AI_PROVIDER_OPTIONS": `{"a":1}`, "AI_MODEL": "m",
		"AI_MODEL_PRICES": `{"m":{"input_micros_per_million_tokens":1,"output_micros_per_million_tokens":1}}`,
		"EMBED_ENDPOINT":  "http://m/v1", "EMBED_MODEL": "e",
		"AUTO_MERGE_ENABLED": "true", "AUTO_MERGE_MAX_FILES_CHANGED": "7",
		"MAINTENANCE_INTERVAL_HOURS":       "9",
		"NF_GATE_ANALYSIS_IMAGE":           "registry.example/analysis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"NF_OPENBAO_CONFIG_FILE":           "/etc/novaforge/openbao.json",
		"NF_MCP_HTTP_CONFIG_FILE":          "/etc/novaforge/mcp.json",
		"NF_REVIEW_CONFIG_FILE":            "/etc/novaforge/review.json",
		"NF_DEPLOYMENT_CONFIG_FILE":        "/etc/novaforge/deployment.json",
		"NF_SEMANTIC_PRODUCER_CONFIG_FILE": "/etc/novaforge/semantic.json",
		"JWT_SECRET":                       "j", "HMAC_SECRET": "h", "SECRETS_KEK": "kek",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	cfg := service.LoadConfig()
	rv := reflect.ValueOf(cfg)
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Errorf("Config.%s is zero after LoadConfig with every variable set: "+
				"the field is declared but its environment variable is never read",
				rv.Type().Field(i).Name)
		}
	}
}
