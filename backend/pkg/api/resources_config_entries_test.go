package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestConfigMapEntriesCarryTheirValues(t *testing.T) {
	view, err := configMapEntries([]byte(`{
		"immutable": true,
		"data": {"queue": "orders", "app.yaml": "workers: 2\n"},
		"binaryData": {"logo.png": "iVBORw0KGgo="}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !view.ValuesShown || !view.Immutable || len(view.Entries) != 3 {
		t.Fatalf("unexpected view: %+v", view)
	}
	// Sorted by key.
	if view.Entries[0].Key != "app.yaml" || *view.Entries[0].Value != "workers: 2\n" {
		t.Fatalf("expected app.yaml first with its value, got %+v", view.Entries[0])
	}
	logo := view.Entries[1]
	if logo.Key != "logo.png" || !logo.Binary || logo.Value != nil || logo.Bytes != 8 {
		t.Fatalf("expected binary data as a size, never a value, got %+v", logo)
	}
}

func TestConfigMapEntriesTruncateOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("ş", configValueLimit) // two bytes each
	body, _ := json.Marshal(map[string]any{"data": map[string]string{"big": long}})
	view, err := configMapEntries(body)
	if err != nil {
		t.Fatal(err)
	}
	entry := view.Entries[0]
	if !entry.Truncated || entry.Bytes != len(long) || len(*entry.Value) > configValueLimit {
		t.Fatalf("expected a truncated value under the limit, got bytes=%d len=%d truncated=%v",
			entry.Bytes, len(*entry.Value), entry.Truncated)
	}
	if !strings.HasSuffix(*entry.Value, "ş") {
		t.Fatal("the value was cut through a character")
	}
}

// A Secret's value never enters this response — only its key and size.
func TestSecretEntriesNeverCarryAValue(t *testing.T) {
	body := []byte(`{"type":"kubernetes.io/basic-auth","data":{"password":"aHVudGVyMg==","username":"YWRtaW4="}}`)
	view, err := secretEntries(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	for _, leaked := range []string{"hunter2", "aHVudGVyMg", "admin", "YWRtaW4"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("a Secret value reached the response: %s", encoded)
		}
	}
	if view.ValuesShown || view.Type != "kubernetes.io/basic-auth" || view.Entries[0].Key != "password" || view.Entries[0].Bytes != 7 {
		t.Fatalf("unexpected view: %+v", view)
	}
}

func TestConfigEntriesRefuseOtherKinds(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/config/entries?kind=pods&namespace=shop&name=api",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
