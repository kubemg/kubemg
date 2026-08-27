package api

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * This is the one route in the resource API that hands a credential to a
 * browser, so what is pinned here is everything standing in front of it: the
 * capability, the two kinds of Secret that are never revealed, the refusals a
 * scoped grant gets, the trail a refusal still leaves, and the fact that no
 * layer is allowed to keep a copy.
 */

const secretValuePath = "/resources/secret/value"

// revealEnv is a cluster, an auditor and a caller holding the capability.
func revealEnv(t *testing.T) (*testEnv, *recordingAuditor, *db.Cluster) {
	t.Helper()
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(opts *Options) { opts.Auditor = auditor })
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	return env, auditor, cluster
}

func TestRevealingASecretNeedsTheCapabilityAndNotMerelyTheAdminRole(t *testing.T) {
	env, auditor, cluster := revealEnv(t)
	// A plain administrator. Administering KubeMG is not the same claim as
	// reading the database password, which is the whole reason this is a
	// capability rather than a tier of the role.
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+"?namespace=shop&name=db&key=password",
		env.tokenFor(t, admin), nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for an admin without the capability, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "super admin") {
		t.Fatalf("the refusal has to say who grants this, got %q", body["error"])
	}

	// A refusal is the line an auditor is looking for. One that leaves no
	// record makes the capability unanswerable after the fact.
	events := eventsFor(auditor, verbSecretReveal)
	if len(events) != 1 {
		t.Fatalf("expected one %s record for the refusal, got %d", verbSecretReveal, len(events))
	}
	if events[0].Status != http.StatusForbidden || events[0].Error == "" {
		t.Fatalf("the record must carry the refusal: %+v", events[0])
	}
	if events[0].Username != "admin" || events[0].ClusterID != cluster.ID {
		t.Fatalf("the record must name the caller and the cluster: %+v", events[0])
	}
	// The Secret and the key are named, because a record that says only "a
	// secret in shop" is not an answer when the Secret holds four keys.
	if !strings.Contains(events[0].Path, "name=db") || !strings.Contains(events[0].Path, "key=password") {
		t.Fatalf("the record must name the secret and the key, got %q", events[0].Path)
	}
}

func TestARevealIsNeverCachedAtAnyLayer(t *testing.T) {
	env, _, cluster := revealEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+"?namespace=shop&name=db&key=password",
		env.tokenFor(t, admin), nil)

	// Set before the capability is even checked: a 403 a proxy holds onto
	// outlives the grant that fixes it, and a 200 held anywhere is a second
	// copy of the value.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestTheAgentsOwnRegistrationSecretIsNeverRevealed(t *testing.T) {
	env, auditor, cluster := revealEnv(t)
	root := env.store.addSuperAdmin("root", "secret123")

	// A console that will hand out the token its own tunnel authenticates with
	// is a console that can be talked into minting a second bastion. Refused
	// before anything is read, so the cluster is never even asked.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+
			"?namespace=kubemg-system&name=kubemg-agent&key=cluster-token",
		env.tokenFor(t, root), nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if events := eventsFor(auditor, verbSecretReveal); len(events) != 1 ||
		events[0].Error != "agent registration secret" {
		t.Fatalf("the refusal must be recorded with its reason, got %+v", events)
	}

	// The same name in another namespace is somebody else's Secret and is not
	// this refusal's business.
	rec = env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+
			"?namespace=shop&name=kubemg-agent&key=cluster-token",
		env.tokenFor(t, root), nil)
	if rec.Code == http.StatusForbidden {
		t.Fatal("the refusal must be pinned to the agent's own namespace, not to the name alone")
	}
}

func TestRevealRefusesAnAddressItCannotTrust(t *testing.T) {
	env, _, cluster := revealEnv(t)
	root := env.store.addSuperAdmin("root", "secret123")
	token := env.tokenFor(t, root)

	cases := map[string]string{
		"no name":            "?namespace=shop&key=password",
		"no key":             "?namespace=shop&name=db",
		"traversal in name":  "?namespace=shop&name=../../secrets&key=password",
		"traversal in key":   "?namespace=shop&name=db&key=../x",
		"slash in the name":  "?namespace=shop&name=db/x&key=password",
		"whitespace as name": "?namespace=shop&name=%20&key=password",
	}
	for label, query := range cases {
		t.Run(label, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+query, token, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRevealHonoursTheGrantsNamespaceScope(t *testing.T) {
	env, _, cluster := revealEnv(t)
	user := env.store.addUser("scoped", "secret123", db.RoleUser)
	// The capability is not a way around the scope: it says this account may be
	// shown a value, not which namespaces it may be shown one from.
	user.CanRevealSecrets = true
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+secretValuePath+"?namespace=team-b&name=db&key=password",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for a namespace outside the grant, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
	// Both refusals are 403, so the message is what says which one answered:
	// this must be the scope, not the capability the caller does hold.
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "granted scope") {
		t.Fatalf("expected the scope refusal, got %q", body["error"])
	}
}

func TestRevealRefusesADirectModeCluster(t *testing.T) {
	env, _, _ := revealEnv(t)
	root := env.store.addSuperAdmin("root", "secret123")
	direct := env.store.addCluster("legacy", "dev")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(direct.ID)+secretValuePath+"?namespace=shop&name=db&key=password",
		env.tokenFor(t, root), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

/* --------------------------------------------------- the object's rules --- */

func encodedSecret(values map[string]string) secretObject {
	data := map[string]string{}
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return secretObject{Data: data}
}

func TestAServiceAccountTokenIsNeverRevealed(t *testing.T) {
	object := encodedSecret(map[string]string{"token": "ey.a.b"})
	object.Type = serviceAccountTokenType

	_, refusal := revealValue(object, "shop", "default-token-xyz", "token")
	if refusal == nil || refusal.Status != http.StatusForbidden {
		t.Fatalf("a ServiceAccount token is a live cluster credential, got %+v", refusal)
	}
	// The refusal is on the *type*, so it holds for every key the object has —
	// including ca.crt, which is harmless, because a rule with an exception is a
	// rule somebody will find the exception in.
	if _, refusal := revealValue(object, "shop", "default-token-xyz", "ca.crt"); refusal == nil {
		t.Fatal("the refusal must cover the whole object, not just the token key")
	}
}

func TestAKeyTheSecretDoesNotHoldIsAFourOhFour(t *testing.T) {
	object := encodedSecret(map[string]string{"password": "hunter2"})

	_, refusal := revealValue(object, "shop", "db", "username")
	if refusal == nil || refusal.Status != http.StatusNotFound {
		t.Fatalf("expected a 404 for an absent key, got %+v", refusal)
	}
}

func TestATextValueIsRevealedAsTextAndABinaryOneIsNot(t *testing.T) {
	object := encodedSecret(map[string]string{"password": "hunter2"})
	out, refusal := revealValue(object, "shop", "db", "password")
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if out.Value != "hunter2" || out.Binary || out.Encoded != "" {
		t.Fatalf("out = %+v, want the decoded text and nothing encoded", out)
	}
	if out.Bytes != len("hunter2") || out.Namespace != "shop" || out.Name != "db" || out.Key != "password" {
		t.Fatalf("out = %+v, want the address and the length carried", out)
	}

	// A TLS key's DER rendered as replacement characters is a worse answer than
	// saying it is binary: "is this the right value" cannot be answered from a
	// mangled reveal.
	der := string([]byte{0x30, 0x82, 0xff, 0xfe})
	binary := encodedSecret(map[string]string{"tls.key": der})
	out, refusal = revealValue(binary, "shop", "tls", "tls.key")
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if !out.Binary || out.Value != "" || out.Encoded == "" {
		t.Fatalf("out = %+v, want it marked binary and left encoded", out)
	}
}

func TestAValueTooLargeToEyeballIsRefusedRatherThanStreamed(t *testing.T) {
	object := encodedSecret(map[string]string{"blob": strings.Repeat("a", secretValueLimit+1)})

	_, refusal := revealValue(object, "shop", "big", "blob")
	if refusal == nil || refusal.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected the size refusal, got %+v", refusal)
	}
}

/* ------------------------------------------------------- the capability --- */

func TestOnlyASuperAdminMayGrantTheSecretRevealCapability(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	root := env.store.addSuperAdmin("root", "pw")
	target := env.store.addUser("oncall", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPut, "/api/v1/users/"+itoa(target.ID), env.tokenFor(t, admin),
		map[string]any{"can_reveal_secrets": true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if target.CanRevealSecrets {
		t.Fatal("the capability must not have been granted")
	}

	rec = env.do(t, http.MethodPut, "/api/v1/users/"+itoa(target.ID), env.tokenFor(t, root),
		map[string]any{"can_reveal_secrets": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := decode[userResponse](t, rec); !body.CanRevealSecrets {
		t.Fatal("a super admin's grant must take effect")
	}

	payload := validUserPayload()
	payload["username"] = "reader"
	payload["can_reveal_secrets"] = true
	rec = env.do(t, http.MethodPost, "/api/v1/users", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d creating one, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestSuperAdminHoldsTheSecretRevealCapabilityImplicitly(t *testing.T) {
	env := newTestEnv(t)
	root := env.store.addSuperAdmin("root", "pw")

	// Nothing sets the column. The account that may grant it can grant it to
	// itself, so reporting otherwise would be theatre — and the console draws
	// its affordances from what the server says it will allow.
	rec := env.do(t, http.MethodGet, "/api/v1/auth/me", env.tokenFor(t, root), nil)
	if body := decode[userResponse](t, rec); !body.CanRevealSecrets {
		t.Fatal("a super admin holds the capability implicitly")
	}

	// And a plain admin does not, which is the difference that makes it a
	// capability rather than a rank.
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	rec = env.do(t, http.MethodGet, "/api/v1/auth/me", env.tokenFor(t, admin), nil)
	if body := decode[userResponse](t, rec); body.CanRevealSecrets {
		t.Fatal("an admin must not hold the capability by role alone")
	}
}
