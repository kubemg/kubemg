package observability

import (
	"context"
	"testing"
)

// The in-cluster shape: Alertmanager reached through the API server's Service
// proxy, so the path asked for is the proxy subresource.
func TestAlertsAreReadThroughTheServiceProxy(t *testing.T) {
	target := Target{
		Kind: "alerts", Provider: "alertmanager", AccessMode: "in-cluster", AuthMode: "none",
		ServiceNamespace: "monitoring", ServiceName: "alertmanager", ServicePort: "9093",
	}
	var asked string
	tunnel := func(_ context.Context, method, path string, _ []byte) (int, []byte, error) {
		asked = method + " " + path
		return 200, []byte(`[{"fingerprint":"f","labels":{"alertname":"X","namespace":"a"},"status":{"state":"suppressed","silencedBy":["s1"]}}]`), nil
	}
	alerts, err := ListAlerts(context.Background(), target, tunnel, Scope{}, AlertFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := "GET /api/v1/namespaces/monitoring/services/http:alertmanager:9093/proxy/api/v2/alerts?active=true&silenced=true&inhibited=true&unprocessed=false"
	if asked != want {
		t.Fatalf("asked %q, want %q", asked, want)
	}
	if len(alerts) != 1 || alerts[0].State != AlertSilenced {
		t.Fatalf("alerts = %+v, want one silenced", alerts)
	}
}

func TestAnObjectFilterRefusesNamesThatAreNotNames(t *testing.T) {
	tunnel := func(context.Context, string, string, []byte) (int, []byte, error) { return 200, []byte(`[]`), nil }
	target := Target{AccessMode: "in-cluster", ServiceNamespace: "m", ServiceName: "am", ServicePort: "9093"}
	if _, err := ListAlerts(context.Background(), target, tunnel, Scope{}, AlertFilter{Namespace: "a", Name: "x.*"}); err == nil {
		t.Fatal("a regex in an object name must be refused, never quoted")
	}
}

func TestPinnedNamespaceIgnoresRegexAndNegativeMatchers(t *testing.T) {
	no := false
	cases := map[string][]SilenceMatcher{
		"":  {{Name: "namespace", Value: "a|b", IsRegex: true}},
		"x": {{Name: "alertname", Value: "Y"}, {Name: "namespace", Value: "x"}},
	}
	cases["  "] = []SilenceMatcher{{Name: "namespace", Value: "a", IsEqual: &no}}
	for want, matchers := range cases {
		if want == "  " {
			want = ""
		}
		if got := pinnedNamespace(matchers); got != want {
			t.Fatalf("pinnedNamespace(%v) = %q, want %q", matchers, got, want)
		}
	}
}
