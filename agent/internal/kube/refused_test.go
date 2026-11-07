package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDialUpgradeRefusedYieldsRefusedError(t *testing.T) {
	// A server that refuses the upgrade with 403 and a Kubernetes Status body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"kind":       "Status",
			"apiVersion": "v1",
			"status":     "Failure",
			"message":    `pods "test" is forbidden: User "kubemg:u:tester" cannot create resource "pods/exec"`,
			"reason":     "Forbidden",
			"code":       403,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{APIURL: server.URL, TokenPath: token, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}

	_, _, dialErr := client.DialUpgrade(context.Background(), "/api/v1/namespaces/default/pods/test/exec", nil, nil)
	if dialErr == nil {
		t.Fatal("expected error, got nil")
	}
	var refused *RefusedError
	if !errors.As(dialErr, &refused) {
		t.Fatalf("expected *RefusedError, got %T: %v", dialErr, dialErr)
	}
	if refused.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", refused.Status, http.StatusForbidden)
	}
	if refused.Message == "" {
		t.Error("Message is empty, want the cluster's forbidden message")
	}
}

func TestDialUpgradeRefusedWithoutBodyYieldsRefusedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{APIURL: server.URL, TokenPath: token, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}

	_, _, dialErr := client.DialUpgrade(context.Background(), "/api/v1/namespaces/default/pods/test/exec", nil, nil)
	if dialErr == nil {
		t.Fatal("expected error, got nil")
	}
	var refused *RefusedError
	if !errors.As(dialErr, &refused) {
		t.Fatalf("expected *RefusedError, got %T: %v", dialErr, dialErr)
	}
	if refused.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", refused.Status, http.StatusUnauthorized)
	}
}
