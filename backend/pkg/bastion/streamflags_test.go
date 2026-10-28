package bastion

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// throughSelection replays what the gateway recorded into a store auditor whose
// audit_verbs selection leaves list (and watch) out, and returns what reached
// the queue and how many records the selection suppressed.
func throughSelection(events []Event) ([]db.AuditEvent, int64) {
	policy := auditpolicy.New()
	policy.Store(auditpolicy.NewSnapshot([]string{"get"}, true))
	auditor := NewStoreAuditor(&queueSink{}, nil, policy)
	for _, event := range events {
		auditor.Record(context.Background(), event)
	}
	return drain(auditor), auditor.Suppressed()
}

// A watch the API server streams on is a watch at the gateway too, however the
// client spells it: it takes the stream path, it is recorded as a streaming
// "watch", and so no audit_verbs selection can drop it.
func TestProxyTreatsEverySpellingOfWatchAsAWatch(t *testing.T) {
	for _, query := range []string{"watch=t", "watch=True", "watch=yes", "watch="} {
		t.Run(query, func(t *testing.T) {
			h := newHarness(t)
			h.addCluster(1, "prod-eu", "kmg_valid")
			admin := h.addUser(10, "admin", db.SystemRoleAdmin)

			agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
				echoStream(`{"type":"ADDED"}`+"\n"))
			if err != nil {
				t.Fatalf("dial agent: %v", err)
			}
			waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

			resp := h.proxyRequest(http.MethodGet,
				"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?"+query, h.token(admin))
			body, _ := io.ReadAll(resp.Body)
			if string(body) != `{"type":"ADDED"}`+"\n" {
				t.Fatalf("watch body did not stream through: %q", body)
			}
			if opens := agent.streamOpens(); len(opens) != 1 || len(agent.requests()) != 0 {
				t.Fatalf("want one stream open and no request/response pair, got %d opens and %d requests",
					len(opens), len(agent.requests()))
			}

			waitFor(t, func() bool { return len(h.audit.events()) >= 2 })
			events := h.audit.events()
			for _, event := range events {
				if event.Verb != "watch" || !event.Streaming {
					t.Fatalf("want a streaming watch record, got verb %q streaming %v", event.Verb, event.Streaming)
				}
			}
			queued, suppressed := throughSelection(events)
			if len(queued) != len(events) || suppressed != 0 {
				t.Fatalf("a watch must survive a selection without list: %d of %d queued, %d suppressed",
					len(queued), len(events), suppressed)
			}
		})
	}
}

// The other side of the same rule: a watch the API server leaves off is an
// ordinary list, and the selection is free to drop it.
func TestProxyTreatsWatchFalseAsAList(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse(`{"items":[]}`), echoStream())
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?watch=False", h.token(admin))
	_, _ = io.ReadAll(resp.Body)
	if len(agent.streamOpens()) != 0 || len(agent.requests()) != 1 {
		t.Fatalf("want one request/response pair and no stream, got %d requests and %d opens",
			len(agent.requests()), len(agent.streamOpens()))
	}

	waitFor(t, func() bool { return len(h.audit.events()) >= 1 })
	events := h.audit.events()
	if len(events) != 1 || events[0].Verb != "list" || events[0].Streaming {
		t.Fatalf("want one non-streaming list record, got %+v", events)
	}
	if queued, suppressed := throughSelection(events); len(queued) != 0 || suppressed != 1 {
		t.Fatalf("a list is the selection's to drop: %d queued, %d suppressed", len(queued), suppressed)
	}
}

// A followed log the API server streams has to stream through the gateway, or
// it is buffered whole and nothing reaches kubectl until the proxy times out.
func TestProxyStreamsAFollowedLogHoweverItIsSpelled(t *testing.T) {
	for _, query := range []string{"follow=True", "follow=t", "follow"} {
		t.Run(query, func(t *testing.T) {
			h := newHarness(t)
			h.addCluster(1, "prod-eu", "kmg_valid")
			admin := h.addUser(10, "admin", db.SystemRoleAdmin)

			agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
				echoStream("line one\n", "line two\n"))
			if err != nil {
				t.Fatalf("dial agent: %v", err)
			}
			waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

			resp := h.proxyRequest(http.MethodGet,
				"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/log?"+query, h.token(admin))
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "line one\nline two\n" {
				t.Fatalf("log stream did not arrive intact: %q", body)
			}
			if opens := agent.streamOpens(); len(opens) != 1 || len(agent.requests()) != 0 {
				t.Fatalf("want one stream open and no request/response pair, got %d opens and %d requests",
					len(opens), len(agent.requests()))
			}

			waitFor(t, func() bool { return len(h.audit.events()) >= 2 })
			for _, event := range h.audit.events() {
				if event.Verb != "log" || !event.Streaming {
					t.Fatalf("want a streaming log record, got verb %q streaming %v", event.Verb, event.Streaming)
				}
			}
		})
	}
}
