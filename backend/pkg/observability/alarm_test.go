package observability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/* ---------------------------------------------------------------- store --- */

type fakeAlarmStore struct {
	mu       sync.Mutex
	rules    []db.AlarmRule
	channels []db.AlarmChannel

	delivered []string
	fired     []uint
}

func (f *fakeAlarmStore) ListAlarmRules(context.Context) ([]db.AlarmRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rules, nil
}

func (f *fakeAlarmStore) ListAlarmChannels(context.Context) ([]db.AlarmChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channels, nil
}

func (f *fakeAlarmStore) RecordAlarmDelivery(_ context.Context, _ uint, status, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, status)
	return nil
}

func (f *fakeAlarmStore) RecordAlarmFired(_ context.Context, id uint, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = append(f.fired, id)
	return nil
}

func (f *fakeAlarmStore) deliveries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.delivered...)
}

/* -------------------------------------------------------------- matching --- */

func TestMatchesRequiresTheSameStream(t *testing.T) {
	rule := db.AlarmRule{Trigger: db.TriggerAudit, DeniedOnly: true}
	signal := Signal{Source: SourceClusterEvent, Reason: "OOMKilled"}
	if Matches(rule, signal) {
		t.Fatal("an audit rule must not match a cluster event")
	}
}

func TestMatchesClusterEvent(t *testing.T) {
	rule := db.AlarmRule{
		Trigger:      db.TriggerClusterEvent,
		EventReasons: "OOMKilled,FailedScheduling",
		EventType:    "Warning",
	}

	hit := Signal{
		Source: SourceClusterEvent,
		Type:   "Warning",
		Reason: "OOMKilled",
	}
	if !Matches(rule, hit) {
		t.Fatal("a listed reason of the right type should match")
	}

	// Reason matching is case-insensitive, because an Event's reason is written by
	// whichever controller emitted it.
	if !Matches(rule, Signal{Source: SourceClusterEvent, Type: "warning", Reason: "oomkilled"}) {
		t.Fatal("reason and type matching should be case-insensitive")
	}

	if Matches(rule, Signal{Source: SourceClusterEvent, Type: "Warning", Reason: "BackOff"}) {
		t.Fatal("an unlisted reason should not match")
	}
	if Matches(rule, Signal{Source: SourceClusterEvent, Type: "Normal", Reason: "OOMKilled"}) {
		t.Fatal("the wrong event type should not match")
	}
}

func TestMatchesNarrowsByClusterAndNamespace(t *testing.T) {
	rule := db.AlarmRule{
		Trigger:      db.TriggerClusterEvent,
		EventReasons: "OOMKilled",
		ClusterID:    7,
		Namespaces:   "payments,checkout",
	}

	base := Signal{Source: SourceClusterEvent, Reason: "OOMKilled", ClusterID: 7, Namespace: "payments"}
	if !Matches(rule, base) {
		t.Fatal("the named cluster and namespace should match")
	}

	other := base
	other.ClusterID = 8
	if Matches(rule, other) {
		t.Fatal("another cluster should not match")
	}

	elsewhere := base
	elsewhere.Namespace = "kube-system"
	if Matches(rule, elsewhere) {
		t.Fatal("an unlisted namespace should not match")
	}

	// A rule with no cluster covers every cluster, including ones registered
	// later — that is the whole point of leaving it unset.
	fleetWide := rule
	fleetWide.ClusterID = 0
	if !Matches(fleetWide, other) {
		t.Fatal("an unscoped rule should cover every cluster")
	}
}

func TestMatchesAuditDeniedOnly(t *testing.T) {
	rule := db.AlarmRule{Trigger: db.TriggerAudit, DeniedOnly: true, Verbs: "delete,exec"}

	denied := Signal{Source: SourceAudit, Verb: "delete", Status: 403, Denied: true}
	if !Matches(rule, denied) {
		t.Fatal("a refused delete should match")
	}
	allowed := denied
	allowed.Denied = false
	allowed.Status = 200
	if Matches(rule, allowed) {
		t.Fatal("a permitted call should not match a denied-only rule")
	}
	wrongVerb := denied
	wrongVerb.Verb = "list"
	if Matches(rule, wrongVerb) {
		t.Fatal("an unlisted verb should not match")
	}
}

func TestMatchesRefusesAnUnknownTrigger(t *testing.T) {
	// A rule this build cannot evaluate must match nothing. Falling through to
	// "everything" would page on every signal after a downgrade.
	rule := db.AlarmRule{Trigger: "future_thing"}
	if Matches(rule, Signal{Source: "future_thing"}) {
		t.Fatal("an unknown trigger must not match")
	}
}

/* -------------------------------------------------------------- delivery --- */

// receiver captures what a channel endpoint was sent.
type receiver struct {
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	status int
}

func (r *receiver) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.heads = append(r.heads, req.Header.Clone())
		status := r.status
		r.mu.Unlock()

		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	}
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *receiver) last() ([]byte, http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil, nil
	}
	return r.bodies[len(r.bodies)-1], r.heads[len(r.heads)-1]
}

func newTestDispatcher(t *testing.T, store *fakeAlarmStore) *Dispatcher {
	t.Helper()
	d := NewDispatcher(DispatcherOptions{
		Store:  store,
		Origin: "https://kubemg.example.com",
	})
	d.Refresh(context.Background())
	return d
}

func TestDispatcherDeliversAMatchingSignal(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	store := &fakeAlarmStore{
		channels: []db.AlarmChannel{{
			ID: 1, Name: "am", Kind: db.ChannelAlertmanager,
			URL: server.URL, AuthMode: db.AuthNone, Enabled: true,
		}},
		rules: []db.AlarmRule{{
			ID: 1, Name: "oom", ChannelID: 1, Enabled: true,
			Trigger: db.TriggerClusterEvent, EventReasons: "OOMKilled",
			Severity: db.SeverityCritical,
		}},
	}

	d := newTestDispatcher(t, store)
	d.deliver(context.Background(), Signal{
		Source: SourceClusterEvent, At: time.Now().UTC(),
		ClusterID: 3, Cluster: "prod-eu", Namespace: "payments",
		Reason: "OOMKilled", Type: "Warning", Object: "pod/api-7d9f",
		Message: "Container api was OOMKilled", Count: 4,
	})

	if got.count() != 1 {
		t.Fatalf("expected one delivery, got %d", got.count())
	}

	body, _ := got.last()
	var alerts []struct {
		Labels       map[string]string `json:"labels"`
		Annotations  map[string]string `json:"annotations"`
		StartsAt     string            `json:"startsAt"`
		GeneratorURL string            `json:"generatorURL"`
	}
	if err := json.Unmarshal(body, &alerts); err != nil {
		t.Fatalf("the Alertmanager payload must be an alert array: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected one alert, got %d", len(alerts))
	}

	alert := alerts[0]
	if alert.Labels["alertname"] != "KubeMGClusterEventOOMKilled" {
		t.Errorf("alertname should name the condition, got %q", alert.Labels["alertname"])
	}
	if alert.Labels["severity"] != db.SeverityCritical {
		t.Errorf("severity should come from the rule, got %q", alert.Labels["severity"])
	}
	if alert.Labels["cluster"] != "prod-eu" || alert.Labels["namespace"] != "payments" {
		t.Errorf("cluster and namespace belong in the labels, got %v", alert.Labels)
	}
	// The object is unbounded cardinality and must not be a routing label.
	if _, isLabel := alert.Labels["object"]; isLabel {
		t.Error("the object must be an annotation, not a label")
	}
	if alert.Annotations["object"] != "pod/api-7d9f" {
		t.Errorf("the object belongs in the annotations, got %v", alert.Annotations)
	}
	if alert.Annotations["occurrences"] != "4" {
		t.Errorf("a repeated event should carry its count, got %v", alert.Annotations)
	}
	if alert.GeneratorURL != "https://kubemg.example.com/explore/3" {
		t.Errorf("a cluster event should link to the cluster, got %q", alert.GeneratorURL)
	}
	if alert.StartsAt == "" {
		t.Error("an alert needs a start time")
	}

	if statuses := store.deliveries(); len(statuses) != 1 || statuses[0] != DeliveryOK {
		t.Errorf("expected one ok delivery recorded, got %v", statuses)
	}
	if len(store.fired) != 1 || store.fired[0] != 1 {
		t.Errorf("the rule should have been stamped, got %v", store.fired)
	}
}

func TestDispatcherDeduplicatesWithinTheCooloff(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	store := &fakeAlarmStore{
		channels: []db.AlarmChannel{{
			ID: 1, Kind: db.ChannelWebhook, URL: server.URL, Enabled: true, AuthMode: db.AuthNone,
		}},
		rules: []db.AlarmRule{{
			ID: 1, Name: "oom", ChannelID: 1, Enabled: true,
			Trigger: db.TriggerClusterEvent, EventReasons: "OOMKilled",
			CooloffSeconds: 300,
		}},
	}
	d := newTestDispatcher(t, store)

	signal := Signal{
		Source: SourceClusterEvent, ClusterID: 1, Cluster: "prod",
		Namespace: "payments", Reason: "OOMKilled", Object: "pod/api",
		Fingerprint: "event/prod/payments/pod/api/OOMKilled",
	}
	for range 5 {
		d.deliver(context.Background(), signal)
	}

	if got.count() != 1 {
		t.Fatalf("a crash loop should collapse into one delivery, got %d", got.count())
	}

	// A different pod is a different problem and must get through.
	other := signal
	other.Object = "pod/worker"
	other.Fingerprint = "event/prod/payments/pod/worker/OOMKilled"
	d.deliver(context.Background(), other)
	if got.count() != 2 {
		t.Fatalf("a different object should deliver, got %d", got.count())
	}
}

func TestDispatcherSkipsDisabledChannelsAndRules(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	store := &fakeAlarmStore{
		channels: []db.AlarmChannel{
			{ID: 1, Kind: db.ChannelWebhook, URL: server.URL, Enabled: false},
			{ID: 2, Kind: db.ChannelWebhook, URL: server.URL, Enabled: true},
		},
		rules: []db.AlarmRule{
			{ID: 1, ChannelID: 1, Enabled: true, Trigger: db.TriggerAudit},
			{ID: 2, ChannelID: 2, Enabled: false, Trigger: db.TriggerAudit},
		},
	}
	d := newTestDispatcher(t, store)
	d.deliver(context.Background(), Signal{Source: SourceAudit, Verb: "delete"})

	if got.count() != 0 {
		t.Fatalf("neither a disabled channel nor a disabled rule should deliver, got %d", got.count())
	}
}

func TestObserveIsANoOpWithNoRules(t *testing.T) {
	// A fleet with no alarms configured must cost the proxy nothing.
	store := &fakeAlarmStore{}
	d := newTestDispatcher(t, store)
	for range 10_000 {
		d.Observe(Signal{Source: SourceAudit, Verb: "list"})
	}
	if len(d.queue) != 0 {
		t.Fatalf("nothing should be queued with no rules armed, got %d", len(d.queue))
	}
}

func TestDeliveryFailureIsRecordedNotRetriedForever(t *testing.T) {
	got := &receiver{status: http.StatusInternalServerError}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	store := &fakeAlarmStore{
		channels: []db.AlarmChannel{{ID: 1, Kind: db.ChannelWebhook, URL: server.URL, Enabled: true}},
		rules: []db.AlarmRule{{
			ID: 1, ChannelID: 1, Enabled: true, Trigger: db.TriggerAudit,
		}},
	}
	d := newTestDispatcher(t, store)
	// Retries are separated by a fixed delay, so this test would otherwise wait it
	// out; one attempt is enough to assert the failure is recorded.
	d.client = &http.Client{Timeout: time.Second}

	if err := d.Test(context.Background(), store.channels[0], "admin"); err == nil {
		t.Fatal("a 500 from the endpoint should be reported as a failure")
	}
	if statuses := store.deliveries(); len(statuses) != 1 || statuses[0] != DeliveryFailed {
		t.Fatalf("the failure should be recorded on the channel, got %v", statuses)
	}
	if got.count() != 1 {
		t.Fatalf("Test sends exactly one attempt, got %d", got.count())
	}
}

/* -------------------------------------------------------------- payloads --- */

func TestPagerDutyCarriesTheRoutingKeyInTheBody(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	channel := db.AlarmChannel{
		ID: 1, Kind: db.ChannelPagerDuty, URL: server.URL,
		AuthMode: db.AuthKey, Secret: "routing-key-abc", Enabled: true,
	}
	store := &fakeAlarmStore{channels: []db.AlarmChannel{channel}}
	d := newTestDispatcher(t, store)

	if err := d.Test(context.Background(), channel, "admin"); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}

	body, header := got.last()
	var event struct {
		RoutingKey  string `json:"routing_key"`
		EventAction string `json:"event_action"`
		DedupKey    string `json:"dedup_key"`
		Payload     struct {
			Summary  string `json:"summary"`
			Severity string `json:"severity"`
			Source   string `json:"source"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("decode PagerDuty payload: %v", err)
	}
	if event.RoutingKey != "routing-key-abc" {
		t.Errorf("the routing key must ride in the body, got %q", event.RoutingKey)
	}
	if event.EventAction != "trigger" {
		t.Errorf("expected a trigger, got %q", event.EventAction)
	}
	if event.DedupKey == "" {
		t.Error("a dedup key is what stops one problem becoming four hundred incidents")
	}
	if event.Payload.Severity != "info" {
		t.Errorf("a test alarm is informational, got %q", event.Payload.Severity)
	}
	// AuthKey must not also set an Authorization header — PagerDuty rejects it.
	if header.Get("Authorization") != "" {
		t.Error("a key-auth channel must not send an Authorization header")
	}
}

func TestBearerAndExtraHeadersAreSent(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	channel := db.AlarmChannel{
		ID: 1, Kind: db.ChannelWebhook, URL: server.URL,
		AuthMode: db.AuthBearer, Secret: "siem-token",
		// The stored header set must not be able to override the credential or the
		// content type; both are dropped by parseHeaders.
		Headers: `{"X-Tenant":"prod","Authorization":"Bearer attacker","Content-Type":"text/plain"}`,
		Enabled: true,
	}
	store := &fakeAlarmStore{channels: []db.AlarmChannel{channel}}
	d := newTestDispatcher(t, store)

	if err := d.Test(context.Background(), channel, "admin"); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}

	_, header := got.last()
	if header.Get("Authorization") != "Bearer siem-token" {
		t.Errorf("the channel's own token must win, got %q", header.Get("Authorization"))
	}
	if header.Get("X-Tenant") != "prod" {
		t.Errorf("an extra header should be sent, got %q", header.Get("X-Tenant"))
	}
	if header.Get("Content-Type") != "application/json" {
		t.Errorf("the content type is not overridable, got %q", header.Get("Content-Type"))
	}
}

func TestWebhookCarriesTheSignalUnreshaped(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	channel := db.AlarmChannel{ID: 1, Kind: db.ChannelWebhook, URL: server.URL, Enabled: true}
	store := &fakeAlarmStore{
		channels: []db.AlarmChannel{channel},
		rules: []db.AlarmRule{{
			ID: 1, Name: "denied", ChannelID: 1, Enabled: true,
			Trigger: db.TriggerAudit, DeniedOnly: true, Severity: db.SeverityCritical,
		}},
	}
	d := newTestDispatcher(t, store)

	d.deliver(context.Background(), Signal{
		Source: SourceAudit, At: time.Now().UTC(),
		ClusterID: 2, Cluster: "prod", Verb: "delete", Username: "dana",
		Status: 403, Denied: true, Path: "/api/v1/namespaces/prod",
		Message: "KubeMG refused DELETE /api/v1/namespaces/prod",
	})

	body, _ := got.last()
	var envelope struct {
		Version  string `json:"version"`
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
		Signal   Signal `json:"signal"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode webhook payload: %v", err)
	}
	if envelope.Version != "kubemg.alarm/v1" {
		t.Errorf("a SIEM needs a versioned shape, got %q", envelope.Version)
	}
	if envelope.Signal.Verb != "delete" || !envelope.Signal.Denied {
		t.Errorf("the signal should arrive intact, got %+v", envelope.Signal)
	}
	if envelope.Signal.Fingerprint == "" {
		t.Error("the fingerprint should be filled in before it leaves")
	}
	if envelope.Severity != db.SeverityCritical {
		t.Errorf("severity comes from the rule, got %q", envelope.Severity)
	}
}

func TestSlackAndServiceNowRenderTheirOwnShapes(t *testing.T) {
	got := &receiver{}
	server := httptest.NewServer(got.handler())
	defer server.Close()

	store := &fakeAlarmStore{}
	d := newTestDispatcher(t, store)

	slack := db.AlarmChannel{ID: 1, Kind: db.ChannelSlack, URL: server.URL, Enabled: true}
	if err := d.Test(context.Background(), slack, "admin"); err != nil {
		t.Fatalf("slack delivery failed: %v", err)
	}
	body, _ := got.last()
	var message struct {
		Text        string `json:"text"`
		Attachments []struct {
			Color string `json:"color"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("decode slack payload: %v", err)
	}
	if message.Text == "" || len(message.Attachments) != 1 {
		t.Fatalf("expected a text line and one attachment, got %+v", message)
	}

	now := db.AlarmChannel{ID: 2, Kind: db.ChannelServiceNow, URL: server.URL, Enabled: true}
	if err := d.Test(context.Background(), now, "admin"); err != nil {
		t.Fatalf("servicenow delivery failed: %v", err)
	}
	body, _ = got.last()
	var incident struct {
		ShortDescription string `json:"short_description"`
		Urgency          string `json:"urgency"`
		Impact           string `json:"impact"`
		CorrelationID    string `json:"correlation_id"`
	}
	if err := json.Unmarshal(body, &incident); err != nil {
		t.Fatalf("decode servicenow payload: %v", err)
	}
	if incident.ShortDescription == "" {
		t.Error("an incident needs a short description")
	}
	// Both are needed or every incident lands on the instance's default priority.
	if incident.Urgency == "" || incident.Impact == "" {
		t.Errorf("urgency and impact are both required, got %+v", incident)
	}
	if incident.CorrelationID == "" {
		t.Error("an ITSM queue that has been flooded needs a human to empty it")
	}
}

func TestUnsupportedKindIsAnError(t *testing.T) {
	d := newTestDispatcher(t, &fakeAlarmStore{})
	_, _, err := d.render(db.AlarmChannel{Kind: "carrier-pigeon"}, db.AlarmRule{}, Signal{})
	if err == nil {
		t.Fatal("an unknown channel kind should not render")
	}
}

/* ------------------------------------------------------------------- url --- */

func TestValidateChannelURL(t *testing.T) {
	valid := []string{
		"https://alertmanager.example.com/api/v2/alerts",
		// Internal addresses are the common case, not something to refuse.
		"http://alertmanager.monitoring.svc:9093/api/v2/alerts",
		"https://10.0.0.5/api/v2/alerts",
	}
	for _, raw := range valid {
		if err := ValidateChannelURL(raw); err != nil {
			t.Errorf("%q should be accepted: %v", raw, err)
		}
	}

	invalid := []string{
		"",
		"alertmanager.example.com",
		"ftp://example.com/hook",
		"file:///etc/passwd",
		// A credential in the userinfo lands in every proxy log on the way.
		"https://user:pass@example.com/hook",
	}
	for _, raw := range invalid {
		if err := ValidateChannelURL(raw); err == nil {
			t.Errorf("%q should be refused", raw)
		}
	}
}
