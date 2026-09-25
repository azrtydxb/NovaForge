package service

import (
	"reflect"
	"testing"
)

// These are paths to operator-mounted documents, never inline credentials. The
// field/env contract is shared by independently configured service entrypoints.
func TestOperatorConfigurationPaths(t *testing.T) {
	paths := map[string]string{
		"OpenBaoConfigFile":          "NF_OPENBAO_CONFIG_FILE",
		"MCPHTTPConfigFile":          "NF_MCP_HTTP_CONFIG_FILE",
		"ReviewConfigFile":           "NF_REVIEW_CONFIG_FILE",
		"DeploymentConfigFile":       "NF_DEPLOYMENT_CONFIG_FILE",
		"SemanticProducerConfigFile": "NF_SEMANTIC_PRODUCER_CONFIG_FILE",
	}
	for _, env := range paths {
		t.Setenv(env, "")
	}
	assertPaths := func(expected map[string]string) {
		t.Helper()
		cfg := reflect.ValueOf(LoadConfig())
		for field := range paths {
			value := cfg.FieldByName(field)
			if !value.IsValid() || value.Kind() != reflect.String {
				t.Errorf("operator configuration field %s is missing", field)
				continue
			}
			if value.String() != expected[field] {
				t.Errorf("%s = %q, want %q", field, value.String(), expected[field])
			}
		}
	}
	assertPaths(nil)
	for field, env := range paths {
		path := "/etc/novaforge/" + field + ".json"
		t.Setenv(env, path)
		assertPaths(map[string]string{field: path})
		t.Setenv(env, "")
	}
}
