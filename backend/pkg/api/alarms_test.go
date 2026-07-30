package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

// withAlarms wires a dispatcher, which the default test stack leaves off — a
// server without one still registers the routes, and the tests that assert the
// "not running" answers depend on that.
func withAlarms(opts *Options) {
	opts.Alarms = observability.NewDispatcher(observability.DispatcherOptions{
		Store: opts.Store.(observability.AlarmStore),
	})
}

func TestAlarmChannelsAreAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	for _, path := range []string{"/api/v1/alarms/channels", "/api/v1/alarms/rules"} {
		rec := env.do(t, http.MethodGet, path, env.tokenFor(t, user), nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected %d for a non-admin, got %d", path, http.StatusForbidden, rec.Code)
		}
	}
}

func TestCreateAlarmChannelNeverReturnsTheSecret(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/alarms/channels", env.tokenFor(t, admin), map[string]any{
		"name":      "oncall",
		"kind":      db.ChannelPagerDuty,
		"url":       "https://events.pagerduty.com/v2/enqueue",
		"auth_mode": db.AuthKey,
		"secret":    "routing-key-abc",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[alarmChannelResponse](t, rec)
	if !body.HasSecret {
		t.Fatal("the response should say a credential is stored")
	}
	// The whole point: an admin console that reads credentials back out turns one
	// compromised session into every integration KubeMG holds.
	if raw := rec.Body.String(); contains(raw, "routing-key-abc") {
		t.Fatalf("the secret must never be serialized: %s", raw)
	}
}

func TestUpdateAlarmChannelKeepsTheStoredSecret(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "siem", Kind: db.ChannelWebhook,
		URL: "https://siem.example.com/in", AuthMode: db.AuthBearer,
		Secret: "kept-token", Enabled: true,
	})

	// Changing the URL with no secret supplied must not clear the credential —
	// otherwise a field nobody can read has to be re-entered to save any change.
	path := fmt.Sprintf("/api/v1/alarms/channels/%d", channel.ID)
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{
		"name":      "siem",
		"kind":      db.ChannelWebhook,
		"url":       "https://siem.example.com/ingest",
		"auth_mode": db.AuthBearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	stored, err := env.store.AlarmChannelByID(t.Context(), channel.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.Secret != "kept-token" {
		t.Fatalf("the stored secret should survive an edit, got %q", stored.Secret)
	}
	if stored.URL != "https://siem.example.com/ingest" {
		t.Fatalf("the URL should have changed, got %q", stored.URL)
	}
}

func TestAlarmChannelURLIsValidated(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	for _, url := range []string{"", "not-a-url", "ftp://example.com/x", "https://u:p@example.com/x"} {
		rec := env.do(t, http.MethodPost, "/api/v1/alarms/channels", env.tokenFor(t, admin), map[string]any{
			"name": "bad", "kind": db.ChannelWebhook, "url": url,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("url %q: expected %d, got %d", url, http.StatusBadRequest, rec.Code)
		}
	}
}

func TestPagerDutyChannelNeedsItsRoutingKey(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/alarms/channels", env.tokenFor(t, admin), map[string]any{
		"name": "oncall", "kind": db.ChannelPagerDuty,
		"url": "https://events.pagerduty.com/v2/enqueue",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDeletingAChannelTakesItsRulesWithIt(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager,
		URL: "https://am.example.com/api/v2/alerts", Enabled: true,
	})
	env.store.addAlarmRule(db.AlarmRule{
		Name: "oom", ChannelID: channel.ID, Enabled: true,
		Trigger: db.TriggerClusterEvent, EventReasons: "OOMKilled",
	})

	path := fmt.Sprintf("/api/v1/alarms/channels/%d", channel.ID)
	rec := env.do(t, http.MethodDelete, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	rules, err := env.store.ListAlarmRules(t.Context())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	// A rule whose channel is gone is a condition that matches and goes nowhere,
	// which looks exactly like coverage right up to the incident nobody was paged
	// for.
	if len(rules) != 0 {
		t.Fatalf("the rule should have gone with its channel, got %d", len(rules))
	}
}

func TestAlarmRuleValidation(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager, URL: "https://am.example.com/x", Enabled: true,
	})
	token := env.tokenFor(t, admin)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"no name", map[string]any{
			"trigger": db.TriggerAudit, "channel_id": channel.ID,
		}},
		{"no channel", map[string]any{
			"name": "r", "trigger": db.TriggerAudit,
		}},
		{"a channel that does not exist", map[string]any{
			"name": "r", "trigger": db.TriggerAudit, "channel_id": 999,
		}},
		{"an unknown trigger", map[string]any{
			"name": "r", "trigger": "telepathy", "channel_id": channel.ID,
		}},
		{"a verb the trail never records", map[string]any{
			"name": "r", "trigger": db.TriggerAudit, "channel_id": channel.ID,
			"verbs": []string{"telekinesis"},
		}},
		{"a cluster-event rule matching everything", map[string]any{
			"name": "r", "trigger": db.TriggerClusterEvent, "channel_id": channel.ID,
		}},
		{"an impossible severity", map[string]any{
			"name": "r", "trigger": db.TriggerAudit, "channel_id": channel.ID,
			"severity": "apocalyptic",
		}},
	}
	for _, tc := range cases {
		rec := env.do(t, http.MethodPost, "/api/v1/alarms/rules", token, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected %d, got %d (%s)",
				tc.name, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateAlarmRuleStoresItsMatchers(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager, URL: "https://am.example.com/x", Enabled: true,
	})

	rec := env.do(t, http.MethodPost, "/api/v1/alarms/rules", env.tokenFor(t, admin), map[string]any{
		"name":        "denied writes",
		"trigger":     db.TriggerAudit,
		"channel_id":  channel.ID,
		"verbs":       []string{"delete", "delete", " create "},
		"denied_only": true,
		"severity":    db.SeverityCritical,
		"namespaces":  []string{"prod", ""},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	rule := decode[db.AlarmRule](t, rec)
	// Duplicates and blanks are dropped: a blank matcher term matches nothing and
	// would silently narrow the rule to zero.
	if rule.Verbs != "delete,create" {
		t.Errorf("expected the verbs deduplicated and trimmed, got %q", rule.Verbs)
	}
	if rule.Namespaces != "prod" {
		t.Errorf("expected the blank namespace dropped, got %q", rule.Namespaces)
	}
	if !rule.DeniedOnly || rule.Severity != db.SeverityCritical {
		t.Errorf("matchers were not stored: %+v", rule)
	}
}

func TestTestingAChannelWithoutADispatcherSaysSo(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager, URL: "https://am.example.com/x", Enabled: true,
	})

	path := fmt.Sprintf("/api/v1/alarms/channels/%d/test", channel.ID)
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d (%s)", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

func TestAlarmRuleListCarriesItsVocabularies(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodGet, "/api/v1/alarms/rules", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	body := decode[struct {
		Rules             []db.AlarmRule `json:"rules"`
		Triggers          []string       `json:"triggers"`
		Severities        []string       `json:"severities"`
		SuggestedReasons  []string       `json:"suggested_reasons"`
		DispatcherRunning bool           `json:"dispatcher_running"`
	}](t, rec)

	// The form's options and the server's validation come from the same place, so
	// they cannot drift apart.
	if len(body.Triggers) != len(db.AlarmTriggers) || len(body.Severities) != len(db.AlarmSeverities) {
		t.Fatalf("the vocabularies should travel with the list: %+v", body)
	}
	if len(body.SuggestedReasons) == 0 {
		t.Error("an operator should be offered the reasons worth alarming on")
	}
	if body.DispatcherRunning {
		t.Error("this stack has no dispatcher and should say so")
	}
}

// contains is strings.Contains, kept local so the assertion above reads as one
// line rather than as an import.
func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestTestingAChannelReachesTheEndpoint(t *testing.T) {
	var got atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer endpoint.Close()

	env := newTestEnvWith(t, withAlarms)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager, URL: endpoint.URL, Enabled: true,
	})

	path := fmt.Sprintf("/api/v1/alarms/channels/%d/test", channel.ID)
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[struct {
		OK bool `json:"ok"`
	}](t, rec)
	if !body.OK {
		t.Fatalf("the endpoint accepted the alarm; the test should say so: %s", rec.Body.String())
	}
	if got.Load() != 1 {
		t.Fatalf("expected exactly one delivery, got %d", got.Load())
	}
}

func TestTestingAChannelReportsARefusalAsAnAnswer(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown routing key", http.StatusBadRequest)
	}))
	defer endpoint.Close()

	env := newTestEnvWith(t, withAlarms)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "pd", Kind: db.ChannelPagerDuty, URL: endpoint.URL,
		AuthMode: db.AuthKey, Secret: "bad-key", Enabled: true,
	})

	path := fmt.Sprintf("/api/v1/alarms/channels/%d/test", channel.ID)
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	// The operator asked whether it works. "No, and here is what the endpoint
	// said" is a successful answer to that question, not a server error.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}](t, rec)
	if body.OK {
		t.Fatal("a 400 from the endpoint is not a working channel")
	}
	if !contains(body.Message, "unknown routing key") {
		t.Fatalf("the endpoint's own words are the useful part, got %q", body.Message)
	}
}
