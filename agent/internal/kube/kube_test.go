package kube

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestURLJoinsPathsWithoutDoubleSlashes(t *testing.T) {
	client, err := New(Options{APIURL: "https://kubernetes.default.svc/", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	cases := map[string]string{
		"/api/v1/pods":                 "https://kubernetes.default.svc/api/v1/pods",
		"api/v1/pods":                  "https://kubernetes.default.svc/api/v1/pods",
		"/api/v1/pods?labelSelector=a": "https://kubernetes.default.svc/api/v1/pods?labelSelector=a",
		"/apis/apps/v1/deployments":    "https://kubernetes.default.svc/apis/apps/v1/deployments",
	}

	for in, want := range cases {
		if got := client.URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewRejectsAnUnreadableCA(t *testing.T) {
	if _, err := New(Options{CAPath: "/nonexistent/ca.crt"}); err == nil {
		t.Fatal("expected a missing cluster CA to be fatal rather than silently insecure")
	}
}

func TestReadBodyEnforcesTheCap(t *testing.T) {
	small := io.NopCloser(strings.NewReader("hello"))
	got, err := ReadBody(small)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("ReadBody = %q", got)
	}

	// A response larger than the cap must fail loudly rather than push the pod
	// past its memory limit.
	oversized := io.NopCloser(bytes.NewReader(make([]byte, maxResponseBody+1)))
	if _, err := ReadBody(oversized); err == nil {
		t.Fatal("expected an oversized response to be refused")
	}
}
