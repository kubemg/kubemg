package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/jit"
)

// The workflow through the HTTP layer, and the refusals that are the point of it.
//
// The tests are written against the same criteria the feature was specified with:
// a non-admin asks, an admin approves, the grant appears, and it stops counting
// when the window ends or somebody hands it back. The negative cases are given the
// same weight, because "an approval workflow" where the requester can approve
// themselves is a form, not a control.

// jitEnv wires the router with the workflow engine, an auditor to assert against,
// and a clock the test controls — an expiry that depends on wall time is a test
// that fails on a slow machine.
type jitEnv struct {
	*testEnv
	auditor *recordingAuditor
	now     func() time.Time
	engine  *jit.Engine
}

func newJitEnv(t *testing.T, clock func() time.Time) *jitEnv {
	t.Helper()

	auditor := &recordingAuditor{}
	var engine *jit.Engine
	env := newTestEnvWith(t, func(opts *Options) {
		store := opts.Store.(*fakeStore)
		// One clock for the fake and the engine. The fake resolves whether a grant
		// is still live, the engine decides when it stops being; a test in which
		// those disagree is testing the test.
		store.now = clock
		engine = jit.New(jit.Options{
			Store:          store,
			Auditor:        auditor,
			CallbackSecret: []byte("callback-secret"),
			ConsoleURL:     "https://kubemg.example.com",
			Now:            clock,
		})
		opts.JIT = engine
		opts.JITCallbackSecret = []byte("callback-secret")
		opts.Auditor = auditor
		// A Slack channel with a signing secret, so the webhook callback tests
		// exercise real signature verification rather than a server with nothing
		// configured to check against.
		store.addAlarmChannel(db.AlarmChannel{
			Kind: db.ChannelSlack, Enabled: true, Secret: string(slackSigningSecret),
		})
	})
	return &jitEnv{testEnv: env, auditor: auditor, now: clock, engine: engine}
}

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// slackSigningSecret is the fake Slack app's signing secret, shared by the
// channel seeded in newJitEnv and by doSlackCallback's signature.
var slackSigningSecret = []byte("slack-signing-secret")

// doSlackCallback posts a JIT webhook callback body signed exactly as Slack
// signs a real request, so the tests exercise the same verification path
// production traffic does rather than bypassing it.
func (e *jitEnv) doSlackCallback(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal callback body: %v", err)
	}
	timestamp := strconv.FormatInt(e.now().Unix(), 10)
	mac := hmac.New(sha256.New, slackSigningSecret)
	mac.Write([]byte("v0:" + timestamp + ":" + string(payload)))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jit/webhooks/callback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// requestBody is the shape the console posts.
func requestBody(clusterID uint, role string, minutes int) map[string]any {
	return map[string]any{
		"cluster_id":       clusterID,
		"requested_role":   role,
		"duration_minutes": minutes,
		"reason":           "investigating the checkout latency incident",
	}
}

func TestJitRequestLifecycle(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	env := newJitEnv(t, fixedClock(start))

	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)

	devToken := env.tokenFor(t, dev)
	adminToken := env.tokenFor(t, admin)

	// 1. A view-only user asks for cluster-admin for half an hour.
	rec := env.do(t, http.MethodPost, "/api/v1/jit/requests", devToken,
		requestBody(cluster.ID, db.K8sRoleClusterAdmin, 30))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create request: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	created := decode[jitRequestResponse](t, rec)
	if created.Status != db.JitStatusPending {
		t.Fatalf("want a pending request, got %q", created.Status)
	}
	if created.ID == "" {
		t.Fatal("a request with no id cannot be approved")
	}
	// Nothing is granted by asking. This is the assertion that says the request is
	// a request and not a self-service escalation.
	if grant, ok := env.store.grantOf(dev.ID, cluster.ID, db.GrantSourceJIT); ok {
		t.Fatalf("a pending request must not carry a grant, got %+v", grant)
	}

	// 2. The requester cannot approve it, whatever else they are.
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/approve", devToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-approval by a non-admin: want 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. The admin approves, and the elevation exists.
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/approve", adminToken,
		map[string]any{"comment": "paged, go ahead"})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	approved := decode[jitRequestResponse](t, rec)
	if approved.Status != db.JitStatusActive {
		t.Fatalf("want an active request after approval, got %q", approved.Status)
	}
	if !approved.Active || approved.RemainingSeconds != 1800 {
		t.Fatalf("want 1800s of window left, got active=%v remaining=%d",
			approved.Active, approved.RemainingSeconds)
	}
	if approved.ApproverUsername != admin.Username {
		t.Fatalf("want the approver recorded, got %q", approved.ApproverUsername)
	}

	grant, ok := env.store.grantOf(dev.ID, cluster.ID, db.GrantSourceJIT)
	if !ok {
		t.Fatal("approval did not write a temporary grant")
	}
	if grant.K8sRole != db.K8sRoleClusterAdmin {
		t.Fatalf("want cluster-admin granted, got %q", grant.K8sRole)
	}
	if grant.ExpiresAt == nil || !grant.ExpiresAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("want the window to end 30 minutes from the approval, got %v", grant.ExpiresAt)
	}

	// 4. The resolver now hands the proxy the elevated role, with the standing
	//    view grant still underneath it.
	access, err := env.store.AccessForUser(t.Context(), dev.ID)
	if err != nil {
		t.Fatalf("resolve access: %v", err)
	}
	if access[cluster.ID].K8sRole != db.K8sRoleClusterAdmin {
		t.Fatalf("want the elevation in force, got %q", access[cluster.ID].K8sRole)
	}

	// 5. The trail carries both steps, and names what was asked for and why.
	assertAudited(t, env, jit.VerbRequest, dev.Username)
	assertAudited(t, env, jit.VerbApprove, admin.Username)
}

// TestJitExpiryRevertsAccess is criterion four: the window ends and the standing
// grant is what is left, with nobody having had to restore it.
func TestJitExpiryRevertsAccess(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	clock := start
	env := newJitEnv(t, func() time.Time { return clock })

	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)

	rec := env.do(t, http.MethodPost, "/api/v1/jit/requests", env.tokenFor(t, dev),
		requestBody(cluster.ID, db.K8sRoleEdit, 30))
	created := decode[jitRequestResponse](t, rec)
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/approve",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	// Past the window, before any sweep has run: the resolver must already refuse
	// to count it. This is the assertion that the sweeper is housekeeping rather
	// than enforcement.
	clock = start.Add(31 * time.Minute)
	access, err := env.store.AccessForUser(t.Context(), dev.ID)
	if err != nil {
		t.Fatalf("resolve access: %v", err)
	}
	if access[cluster.ID].K8sRole != db.K8sRoleView {
		t.Fatalf("want the standing view grant back, got %q", access[cluster.ID].K8sRole)
	}

	// And the sweep closes the row out and records it.
	env.engine.Sweep(t.Context())
	stored, err := env.store.JitRequestByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("reload request: %v", err)
	}
	if stored.Status != db.JitStatusExpired {
		t.Fatalf("want an expired request after the sweep, got %q", stored.Status)
	}
	assertAudited(t, env, jit.VerbExpire, dev.Username)
}

// TestJitRevokeEndsElevationEarly covers the other half of criterion four, and the
// rule that giving privilege back never needs permission.
func TestJitRevokeEndsElevationEarly(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	env := newJitEnv(t, fixedClock(start))

	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	other := env.store.addUser("other", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)

	rec := env.do(t, http.MethodPost, "/api/v1/jit/requests", env.tokenFor(t, dev),
		requestBody(cluster.ID, db.K8sRoleClusterAdmin, 60))
	created := decode[jitRequestResponse](t, rec)
	env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/approve",
		env.tokenFor(t, admin), nil)

	// Somebody else's elevation is not theirs to end.
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/revoke",
		env.tokenFor(t, other), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoking somebody else's grant: want 403, got %d", rec.Code)
	}

	// The holder hands it back.
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/revoke",
		env.tokenFor(t, dev), map[string]any{"comment": "finished"})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke own grant: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	revoked := decode[jitRequestResponse](t, rec)
	if revoked.Status != db.JitStatusRevoked || revoked.Active {
		t.Fatalf("want a revoked, inactive request, got %q active=%v", revoked.Status, revoked.Active)
	}
	if _, ok := env.store.grantOf(dev.ID, cluster.ID, db.GrantSourceJIT); ok {
		t.Fatal("revoking left the temporary grant behind")
	}
	access, _ := env.store.AccessForUser(t.Context(), dev.ID)
	if access[cluster.ID].K8sRole != db.K8sRoleView {
		t.Fatalf("want view after revocation, got %q", access[cluster.ID].K8sRole)
	}

	// Revoking twice is a conflict, not a second revocation.
	rec = env.do(t, http.MethodPost, "/api/v1/jit/requests/"+created.ID+"/revoke",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second revoke: want 409, got %d", rec.Code)
	}
}

func TestJitRequestValidation(t *testing.T) {
	env := newJitEnv(t, fixedClock(time.Now().UTC()))
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)
	token := env.tokenFor(t, dev)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{
			name: "an unknown role",
			body: map[string]any{"cluster_id": cluster.ID, "requested_role": "root",
				"duration_minutes": 30, "reason": "because I need it badly"},
			want: http.StatusBadRequest,
		},
		{
			// A window measured in days is not just-in-time access.
			name: "a window past the ceiling",
			body: map[string]any{"cluster_id": cluster.ID, "requested_role": db.K8sRoleEdit,
				"duration_minutes": 5000, "reason": "because I need it badly"},
			want: http.StatusBadRequest,
		},
		{
			// The reason is the whole value of the record.
			name: "no reason worth storing",
			body: map[string]any{"cluster_id": cluster.ID, "requested_role": db.K8sRoleEdit,
				"duration_minutes": 30, "reason": "fix"},
			want: http.StatusBadRequest,
		},
		{
			name: "a cluster that does not exist",
			body: map[string]any{"cluster_id": 4242, "requested_role": db.K8sRoleEdit,
				"duration_minutes": 30, "reason": "because I need it badly"},
			want: http.StatusBadRequest,
		},
		{
			// Asking for what a standing grant already covers spends an approver's
			// attention on nothing.
			name: "access already held",
			body: map[string]any{"cluster_id": cluster.ID, "requested_role": db.K8sRoleView,
				"duration_minutes": 30, "reason": "because I need it badly"},
			want: http.StatusConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, "/api/v1/jit/requests", token, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}

	// One open ask per cluster, so an approver never has to guess which duplicate
	// grants the window.
	first := env.do(t, http.MethodPost, "/api/v1/jit/requests", token,
		requestBody(cluster.ID, db.K8sRoleEdit, 30))
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: %d %s", first.Code, first.Body.String())
	}
	second := env.do(t, http.MethodPost, "/api/v1/jit/requests", token,
		requestBody(cluster.ID, db.K8sRoleEdit, 30))
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate request: want 409, got %d", second.Code)
	}
}

// TestJitListNarrowsToOwnRequests is the audit trail's rule applied here: a
// pending request carries somebody's stated reason for needing production.
func TestJitListNarrowsToOwnRequests(t *testing.T) {
	env := newJitEnv(t, fixedClock(time.Now().UTC()))
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	nosy := env.store.addUser("nosy", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	env.store.addJitRequest(db.JitRequest{
		RequesterID: dev.ID, RequesterUsername: dev.Username,
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		RequestedRole: db.K8sRoleEdit, DurationMinutes: 30, Reason: "incident",
	})

	// Even naming the other account explicitly: the parameter cannot widen the
	// narrowing, exactly as on /audit.
	rec := env.do(t, http.MethodGet, "/api/v1/jit/requests?user_id="+itoa(dev.ID),
		env.tokenFor(t, nosy), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Requests   []jitRequestResponse `json:"requests"`
		CanApprove bool                 `json:"can_approve"`
		ScopedToMe bool                 `json:"scoped_to_me"`
	}](t, rec)
	if len(body.Requests) != 0 {
		t.Fatalf("a non-admin must not see somebody else's requests, got %d", len(body.Requests))
	}
	if body.CanApprove || !body.ScopedToMe {
		t.Fatalf("want a narrowed, non-approving view, got can_approve=%v scoped=%v",
			body.CanApprove, body.ScopedToMe)
	}

	rec = env.do(t, http.MethodGet, "/api/v1/jit/requests", env.tokenFor(t, admin), nil)
	body = decode[struct {
		Requests   []jitRequestResponse `json:"requests"`
		CanApprove bool                 `json:"can_approve"`
		ScopedToMe bool                 `json:"scoped_to_me"`
	}](t, rec)
	if len(body.Requests) != 1 || !body.CanApprove {
		t.Fatalf("an admin sees the queue and may decide it, got %d requests can_approve=%v",
			len(body.Requests), body.CanApprove)
	}
}

func TestJitRequiresAuthentication(t *testing.T) {
	env := newJitEnv(t, fixedClock(time.Now().UTC()))
	for _, path := range []string{
		"/api/v1/jit/requests",
		"/api/v1/jit/requests/whatever/approve",
	} {
		rec := env.do(t, http.MethodPost, path, "", requestBody(1, db.K8sRoleEdit, 30))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: want 401, got %d", path, rec.Code)
		}
	}
}

// TestJitWebhookCallback covers the chat path: a signed token plus an identity
// that resolves to an administrator, and every way of having only one of them.
func TestJitWebhookCallback(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	env := newJitEnv(t, fixedClock(start))
	secret := []byte("callback-secret")

	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	seed := func() *db.JitRequest {
		return env.store.addJitRequest(db.JitRequest{
			RequesterID: dev.ID, RequesterUsername: dev.Username,
			ClusterID: cluster.ID, ClusterName: cluster.Name,
			RequestedRole: db.K8sRoleClusterAdmin, DurationMinutes: 60, Reason: "incident",
		})
	}

	t.Run("a signed approval from an admin", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(time.Hour),
		})
		rec := env.doSlackCallback(t, map[string]any{
			"token":             token,
			"approver_username": admin.Username,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("callback: want 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if _, ok := env.store.grantOf(dev.ID, cluster.ID, db.GrantSourceJIT); !ok {
			t.Fatal("a webhook approval did not activate the grant")
		}
		// The record names the administrator, not the integration.
		assertAudited(t, env, jit.VerbApprove, admin.Username)
	})

	t.Run("a forged token", func(t *testing.T) {
		request := seed()
		token := jit.SignAction([]byte("not-the-secret"), jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(time.Hour),
		})
		rec := env.doSlackCallback(t, map[string]any{
			"token": token, "approver_username": admin.Username,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("forged token: want 403, got %d", rec.Code)
		}
	})

	t.Run("an expired token", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(-time.Minute),
		})
		rec := env.doSlackCallback(t, map[string]any{
			"token": token, "approver_username": admin.Username,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expired token: want 403, got %d", rec.Code)
		}
	})

	t.Run("a valid token but not an administrator", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(time.Hour),
		})
		rec := env.doSlackCallback(t, map[string]any{
			"token": token, "approver_username": dev.Username,
		})
		// The requester's own name on their own request: the self-approval rule
		// applies to the webhook exactly as it does to the console.
		if rec.Code != http.StatusForbidden {
			t.Fatalf("self-approval over the webhook: want 403, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a token that does not authorise the claimed action", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionReject, Expires: start.Add(time.Hour),
		})
		rec := env.doSlackCallback(t, map[string]any{
			"token": token, "action": "approve", "approver_username": admin.Username,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("mismatched action: want 403, got %d", rec.Code)
		}
	})

	// This is the vulnerability the signature check exists for: without it, a
	// valid token plus a claimed identity was sufficient, and both travel in
	// the same broadcast notification everyone in the channel can read.
	t.Run("a valid token and identity but no Slack signature at all", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(time.Hour),
		})
		rec := env.do(t, http.MethodPost, "/api/v1/jit/webhooks/callback", "", map[string]any{
			"token":             token,
			"approver_username": admin.Username,
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unsigned callback: want 403, got %d: %s", rec.Code, rec.Body.String())
		}
		stored, err := env.store.JitRequestByID(t.Context(), request.ID)
		if err != nil {
			t.Fatalf("re-read request: %v", err)
		}
		if stored.Status != db.JitStatusPending {
			t.Fatalf("an unverified callback must not decide the request, status is %q", stored.Status)
		}
	})

	t.Run("a valid token and identity with a forged Slack signature", func(t *testing.T) {
		request := seed()
		token := jit.SignAction(secret, jit.Action{
			RequestID: request.ID, Action: jit.ActionApprove, Expires: start.Add(time.Hour),
		})
		payload := map[string]any{
			"token":             token,
			"approver_username": admin.Username,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal callback body: %v", err)
		}
		timestamp := strconv.FormatInt(start.Unix(), 10)
		mac := hmac.New(sha256.New, []byte("not-the-slack-signing-secret"))
		mac.Write([]byte("v0:" + timestamp + ":" + string(raw)))
		signature := "v0=" + hex.EncodeToString(mac.Sum(nil))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/jit/webhooks/callback", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Slack-Request-Timestamp", timestamp)
		req.Header.Set("X-Slack-Signature", signature)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("forged signature: want 403, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// assertAudited fails unless the trail holds a record with this verb, written
// against this account.
func assertAudited(t *testing.T, env *jitEnv, verb, username string) {
	t.Helper()
	for _, event := range env.auditor.all() {
		if event.Verb == verb && event.Username == username {
			if event.Resource != "jitrequests" {
				t.Fatalf("%s recorded against %q rather than the request", verb, event.Resource)
			}
			return
		}
	}
	t.Fatalf("no %s audit record for %q; got %+v", verb, username, env.auditor.all())
}
