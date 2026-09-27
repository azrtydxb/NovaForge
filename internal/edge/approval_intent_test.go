package edge

import (
	"encoding/json"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"strings"
	"testing"
)

func TestDeploymentApprovalShowsBoundIntentWithoutUnrelatedDetail(t *testing.T) {
	request := &gatesv1.ApprovalRequestMsg{Action: "deploy_staging", DetailJson: `{"id":"request-1","target":"staging","artifact":"sha256:abc","environment":"staging","destination":"[\"cluster\",\"namespace\",\"release\"]","target_revision":"revision-1","credential":"MUST-NOT-EXPOSE"}`}
	got := ApprovalJSON(request)
	detail, ok := got["deployment"].(map[string]string)
	if !ok || detail["artifact"] != "sha256:abc" || detail["target_revision"] != "revision-1" || detail["id"] != "request-1" {
		t.Fatalf("approval intent missing: %v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "MUST-NOT-EXPOSE") {
		t.Fatal("unrelated raw detail exposed")
	}
	request.Action = "schema_change"
	if _, exists := ApprovalJSON(request)["deployment"]; exists {
		t.Fatal("non-deployment approval misrepresented")
	}
}
