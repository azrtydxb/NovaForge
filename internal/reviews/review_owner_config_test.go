package reviews

import "testing"

func TestReviewConfigAcceptsExplicitExecutionOwner(t *testing.T) {
	config, err := ParseReviewConfig([]byte(`{"execution_owner_url":"https://owner.example/executions/v1","wallclock_seconds":30,"max_input_bytes":4096,"max_output_tokens":1024,"max_concurrent_requests":1,"max_roles":1}`))
	if err != nil {
		t.Fatalf("explicit operator execution owner rejected: %v", err)
	}
	if config.ExecutionOwnerURL != "https://owner.example/executions/v1" {
		t.Fatal("operator execution owner was not preserved")
	}
}
