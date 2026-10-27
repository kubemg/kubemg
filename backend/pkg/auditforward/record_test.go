package auditforward

import (
	"context"
	"encoding/json"
	"testing"
)

// A create names what it made under the structured log's own key, and the
// manifest diff still never leaves.
func TestEncodeCarriesTheObjectNameAndNeverTheDiff(t *testing.T) {
	created := event("create", 201)
	created.Method = "POST"
	created.Resource = "deployments"
	created.Name = "cert-manager"
	created.Diff = []byte(`{"changes":[{"path":"spec.replicas"}]}`)

	rec, ok := encode(context.Background(), created)
	if !ok {
		t.Fatal("encode failed")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.body, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["name"] != "cert-manager" {
		t.Errorf("name = %v, want cert-manager", body["name"])
	}
	if _, present := body["diff"]; present {
		t.Error("the manifest diff must never be forwarded")
	}

	unnamed, _ := encode(context.Background(), event("list", 200))
	var listBody map[string]any
	if err := json.Unmarshal(unnamed.body, &listBody); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, present := listBody["name"]; present {
		t.Error("a list names no object, so it carries no name field")
	}
}
