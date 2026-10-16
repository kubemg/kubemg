package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const (
	mfaWebAuthnPath          = "/api/v1/auth/mfa/webauthn"
	mfaChallengeWebAuthnPath = "/api/v1/auth/mfa/challenge/webauthn"
	// testOrigin is the page the test stack's public URL makes the console.
	testOrigin = "https://kubemg.example.com"
	testRPID   = "kubemg.example.com"
)

// virtualKey is a security key in software: a P-256 key pair, a credential id
// and a signature counter, answering ceremonies the way an authenticator does —
// "none" attestation, user presence, no user verification. The CBOR it speaks
// is written out by hand: it is three small maps, and a test that leans on the
// library it is testing to encode its own input would prove less.
type virtualKey struct {
	key     *ecdsa.PrivateKey
	id      []byte
	counter uint32
	rpID    string
	origin  string
}

func newVirtualKey(t *testing.T) *virtualKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &virtualKey{key: key, id: id, counter: 1, rpID: testRPID, origin: testOrigin}
}

var b64 = base64.RawURLEncoding

func cborBytes(b []byte) []byte {
	switch n := len(b); {
	case n < 24:
		return append([]byte{0x40 | byte(n)}, b...)
	case n < 256:
		return append([]byte{0x58, byte(n)}, b...)
	default:
		return append([]byte{0x59, byte(n >> 8), byte(n)}, b...)
	}
}

func cborText(s string) []byte { return append([]byte{0x60 | byte(len(s))}, s...) }

// coseKey is the key's public half as a COSE_Key: {1: 2 (EC2), 3: -7 (ES256),
// -1: 1 (P-256), -2: x, -3: y}.
func (k *virtualKey) coseKey() []byte {
	x := k.key.X.FillBytes(make([]byte, 32))
	y := k.key.Y.FillBytes(make([]byte, 32))
	out := []byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21}
	out = append(out, cborBytes(x)...)
	out = append(out, 0x22)
	return append(out, cborBytes(y)...)
}

// authData is the authenticator data a signature covers: the relying party's
// hash, the flags, the counter, and — when the key is being registered — the
// credential it made.
func (k *virtualKey) authData(attested bool) []byte {
	rpHash := sha256.Sum256([]byte(k.rpID))
	flags := byte(0x01) // user present
	if attested {
		flags |= 0x40
	}
	out := append(rpHash[:], flags)
	out = binary.BigEndian.AppendUint32(out, k.counter)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID: none
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
		out = append(out, k.coseKey()...)
	}
	return out
}

func (k *virtualKey) clientData(kind string, challenge []byte) []byte {
	data, _ := json.Marshal(map[string]any{
		"type": kind, "challenge": b64.EncodeToString(challenge), "origin": k.origin, "crossOrigin": false,
	})
	return data
}

// register answers a registration ceremony with a "none" attestation.
func (k *virtualKey) register(challenge []byte) json.RawMessage {
	object := []byte{0xa3}
	object = append(object, cborText("fmt")...)
	object = append(object, cborText("none")...)
	object = append(object, cborText("attStmt")...)
	object = append(object, 0xa0)
	object = append(object, cborText("authData")...)
	object = append(object, cborBytes(k.authData(true))...)
	answer, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(k.clientData("webauthn.create", challenge)),
			"attestationObject": b64.EncodeToString(object),
			"transports":        []string{"usb"},
		},
		"clientExtensionResults": map[string]any{},
	})
	return answer
}

// sign answers an assertion ceremony, moving the counter on first as a key
// that keeps one does.
func (k *virtualKey) sign(t *testing.T, challenge []byte) json.RawMessage {
	t.Helper()
	if k.counter != 0 {
		k.counter++
	}
	authData := k.authData(false)
	clientData := k.clientData("webauthn.get", challenge)
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(slices.Clone(authData), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData),
			"authenticatorData": b64.EncodeToString(authData),
			"signature":         b64.EncodeToString(signature),
		},
		"clientExtensionResults": map[string]any{},
	})
	return answer
}

// doFrom is env.do from a page: the request carries the Origin a browser sends,
// and reaches the server at that origin's host — a same-origin request, which
// is what a console served by the server makes.
func (e *testEnv) doFrom(t *testing.T, origin, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = bytes.NewReader(nil)
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	if parsed, err := url.Parse(origin); err == nil {
		req.Host = parsed.Host
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// registerKey enrols a virtual key on a signed-in account that has no factor
// yet, and returns the recovery codes that came with it.
func registerKey(t *testing.T, env *testEnv, token string, key *virtualKey) []string {
	t.Helper()
	rec := env.doFrom(t, testOrigin, http.MethodPost, mfaEnrollPath, token, map[string]string{
		"kind": "webauthn", "name": "YubiKey",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("begin registration: %d %s", rec.Code, rec.Body.String())
	}
	enrol := decode[mfaEnrollResponse](t, rec)
	if enrol.Kind != db.MFAKindWebAuthn || enrol.WebAuthn == nil || enrol.Secret != "" {
		t.Fatalf("a key's enrolment: %+v", enrol)
	}
	if got := enrol.WebAuthn.Response.RelyingParty.ID; got != testRPID {
		t.Fatalf("relying party id %q, want the public URL's host", got)
	}
	rec = env.doFrom(t, testOrigin, http.MethodPost, mfaConfirmPath, token, map[string]any{
		"factor_id": enrol.FactorID, "credential": key.register(enrol.WebAuthn.Response.Challenge),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("finish registration: %d %s", rec.Code, rec.Body.String())
	}
	return decode[mfaConfirmResponse](t, rec).RecoveryCodes
}

// keyProof runs an assertion ceremony — through the sign-in challenge when one
// is given, else on the session — and returns the proof a route takes.
func keyProof(t *testing.T, env *testEnv, token, challenge string, key *virtualKey) webauthnProof {
	t.Helper()
	path, body := mfaWebAuthnPath, map[string]string{}
	if challenge != "" {
		path, body = mfaChallengeWebAuthnPath, map[string]string{"challenge": challenge}
	}
	rec := env.doFrom(t, testOrigin, http.MethodPost, path, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin assertion: %d %s", rec.Code, rec.Body.String())
	}
	opened := decode[webauthnAssertResponse](t, rec)
	if !strings.HasPrefix(opened.Ceremony, mfaChallengePrefix) || opened.Options == nil {
		t.Fatalf("an assertion ceremony: %+v", opened)
	}
	if len(opened.Options.Response.AllowedCredentials) != 1 ||
		!bytes.Equal(opened.Options.Response.AllowedCredentials[0].CredentialID, key.id) {
		t.Fatalf("the ceremony does not name the account's key: %+v", opened.Options.Response.AllowedCredentials)
	}
	return webauthnProof{Ceremony: opened.Ceremony, Credential: key.sign(t, opened.Options.Response.Challenge)}
}

func verifyWithKey(t *testing.T, env *testEnv, challenge string, proof webauthnProof) *httptest.ResponseRecorder {
	t.Helper()
	return env.doFrom(t, testOrigin, http.MethodPost, mfaVerifyPath, "", map[string]any{
		"challenge": challenge, "webauthn": proof,
	})
}

func TestRelyingPartyIsThePublicURLsHost(t *testing.T) {
	cases := []struct {
		url       string
		available bool
		rpID      string
		reason    string
	}{
		{"https://kubemg.example.com", true, "kubemg.example.com", ""},
		{"https://KubeMG.Example.com:8443/console/", true, "kubemg.example.com", ""},
		{"http://localhost:5173", true, "localhost", ""},
		{"http://kubemg.example.com", false, "", "https"},
		{"https://10.0.0.5", false, "", "domain name"},
		{"https://[::1]:8443", false, "", "domain name"},
		{"", false, "", "none is set"},
	}
	for _, tc := range cases {
		_, status := relyingParty(tc.url)
		if status.Available != tc.available || status.RPID != tc.rpID || !strings.Contains(status.Reason, tc.reason) {
			t.Errorf("%q: %+v", tc.url, status)
		}
	}
	if _, status := relyingParty("https://KubeMG.Example.com:8443/x"); status.origin != "https://kubemg.example.com:8443" {
		t.Errorf("origin %q", status.origin)
	}
}

// The ceremony end to end: a key registered on the profile, offered first at the
// next sign-in, and a session that records it.
func TestASecurityKeyRegistersAndSignsIn(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("dev", "pw", db.RoleUser)
	key := newVirtualKey(t)

	codes := registerKey(t, env, env.tokenFor(t, user), key)
	if len(codes) == 0 {
		t.Fatal("a key as the first factor came without recovery codes")
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool {
		return e.Verb == verbMFAEnroll && strings.HasSuffix(e.Path, "?kind=webauthn")
	}) {
		t.Fatal("the trail does not say a key was enrolled")
	}

	body := passwordStep(t, env, "dev", "pw")
	if !body.MFARequired || len(body.Methods) == 0 || body.Methods[0] != "webauthn" {
		t.Fatalf("the key is not offered first: %+v", body)
	}
	if slices.Contains(body.Methods, "totp") {
		t.Fatalf("an app is offered to an account that holds none: %v", body.Methods)
	}
	rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, key))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify with the key: %d %s", rec.Code, rec.Body.String())
	}
	if !sessionClaims(t, env, decode[loginResponse](t, rec).Token).MFA {
		t.Fatal("the session does not record its second factor")
	}
}

// A WebAuthn challenge is good for one signature, and the sign-in challenge it
// sits inside is not spent by a wrong one.
func TestAKeyCeremonyIsSingleUse(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	key := newVirtualKey(t)
	registerKey(t, env, env.tokenFor(t, user), key)

	first := passwordStep(t, env, "dev", "pw")
	proof := keyProof(t, env, "", first.Challenge, key)
	if rec := verifyWithKey(t, env, first.Challenge, proof); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d %s", rec.Code, rec.Body.String())
	}

	second := passwordStep(t, env, "dev", "pw")
	if rec := verifyWithKey(t, env, second.Challenge, proof); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a replayed signature opened a second session: %d", rec.Code)
	}
	// The sign-in challenge survived the refusal.
	if rec := verifyWithKey(t, env, second.Challenge, keyProof(t, env, "", second.Challenge, key)); rec.Code != http.StatusOK {
		t.Fatalf("a fresh ceremony after a refused one: %d %s", rec.Code, rec.Body.String())
	}
}

// A counter that does not move forward is the sign of a cloned key.
func TestACounterRegressionIsRefused(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	key := newVirtualKey(t)
	registerKey(t, env, env.tokenFor(t, user), key)

	body := passwordStep(t, env, "dev", "pw")
	if rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, key)); rec.Code != http.StatusOK {
		t.Fatalf("first signature: %d %s", rec.Code, rec.Body.String())
	}

	// The clone signs with a counter the original has already used.
	key.counter--
	body = passwordStep(t, env, "dev", "pw")
	rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, key))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a counter that went backwards was accepted: %d", rec.Code)
	}
}

// A synced passkey keeps no counter and signs 0 every time; that is accepted,
// as long as the key has never signed with a counter.
func TestAKeyWithoutACounterSignsInRepeatedly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	key := newVirtualKey(t)
	key.counter = 0
	registerKey(t, env, env.tokenFor(t, user), key)

	for range 2 {
		body := passwordStep(t, env, "dev", "pw")
		if rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, key)); rec.Code != http.StatusOK {
			t.Fatalf("a counterless key: %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The answer is bound to the page that asked: a key used on another origin, or
// a signature by a different key, proves nothing.
func TestAKeysAnswerIsBoundToTheOriginAndTheKey(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	key := newVirtualKey(t)
	registerKey(t, env, env.tokenFor(t, user), key)

	phished := *key
	phished.origin = "https://kubemg.example.com.evil.test"
	body := passwordStep(t, env, "dev", "pw")
	if rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, &phished)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a signature made for another origin was accepted: %d", rec.Code)
	}

	impostor := newVirtualKey(t)
	impostor.id = key.id
	if rec := verifyWithKey(t, env, body.Challenge, keyProof(t, env, "", body.Challenge, impostor)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a signature by a different key was accepted: %d", rec.Code)
	}
}

// One account's ceremony proves nothing for another's sign-in.
func TestACeremonyBelongsToItsAccount(t *testing.T) {
	env := newTestEnv(t)
	alice := env.store.addUser("alice", "pw", db.RoleUser)
	bob := env.store.addUser("bob", "pw", db.RoleUser)
	aliceKey, bobKey := newVirtualKey(t), newVirtualKey(t)
	registerKey(t, env, env.tokenFor(t, alice), aliceKey)
	registerKey(t, env, env.tokenFor(t, bob), bobKey)

	aliceSignIn := passwordStep(t, env, "alice", "pw")
	bobSignIn := passwordStep(t, env, "bob", "pw")
	proof := keyProof(t, env, "", aliceSignIn.Challenge, aliceKey)
	if rec := verifyWithKey(t, env, bobSignIn.Challenge, proof); rec.Code != http.StatusUnauthorized {
		t.Fatalf("alice's key signed bob in: %d", rec.Code)
	}
}

func TestAKeyIsRefusedWhereTheConsoleCannotUseOne(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)

	// A page opened somewhere other than the public URL is told where to go.
	rec := env.doFrom(t, "http://localhost:5173", http.MethodPost, mfaEnrollPath, token, map[string]string{"kind": "webauthn"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), testOrigin) {
		t.Fatalf("a foreign origin: %d %s", rec.Code, rec.Body.String())
	}

	env.store.settings[db.SettingPublicURL] = "http://kubemg.example.com"
	rec = env.doFrom(t, "http://kubemg.example.com", http.MethodPost, mfaEnrollPath, token, map[string]string{"kind": "webauthn"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "https") {
		t.Fatalf("a plain-http console: %d %s", rec.Code, rec.Body.String())
	}
	status := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil))
	if status.WebAuthn.Available || status.WebAuthn.Reason == "" {
		t.Fatalf("the profile is not told why: %+v", status.WebAuthn)
	}

	rec = env.do(t, http.MethodPost, mfaEnrollPath, token, map[string]string{"kind": "sms"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind: %d", rec.Code)
	}
}

// A key is bound to the host it was registered at. Once the public URL moves,
// the key is still listed — the person has to see what they hold — but sign-in
// no longer offers it, and a ceremony for it is refused.
func TestAChangedPublicURLRetiresTheKeysRegisteredUnderTheOldOne(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)
	key := newVirtualKey(t)
	registerKey(t, env, token, key)

	env.store.settings[db.SettingPublicURL] = "https://console.example.net"
	body := passwordStep(t, env, "dev", "pw")
	if slices.Contains(body.Methods, "webauthn") {
		t.Fatalf("a key bound to the old host is still offered: %v", body.Methods)
	}
	rec := env.doFrom(t, "https://console.example.net", http.MethodPost, mfaChallengeWebAuthnPath, "",
		map[string]string{"challenge": body.Challenge})
	if rec.Code != http.StatusConflict {
		t.Fatalf("a ceremony with no key for this host: %d", rec.Code)
	}
	status := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil))
	if len(status.Factors) != 1 || status.Factors[0].RPID != testRPID || status.WebAuthn.RPID != "console.example.net" {
		t.Fatalf("the profile cannot tell the key is stale: %+v", status)
	}
}

// Changes to the factors take a key's answer in place of a code: removing one,
// here, with nothing but the key.
func TestAKeyProvesAChangeToTheFactors(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)
	key := newVirtualKey(t)
	registerKey(t, env, token, key)
	status := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil))

	path := "/api/v1/auth/mfa/factors/" + itoa(status.Factors[0].ID)
	if rec := env.do(t, http.MethodDelete, path, token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("removal with no proof: %d", rec.Code)
	}
	rec := env.doFrom(t, testOrigin, http.MethodDelete, path, token, map[string]any{
		"webauthn": keyProof(t, env, token, "", key),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("removal proved by the key: %d %s", rec.Code, rec.Body.String())
	}
	if decode[mfaStatusResponse](t, rec).Enrolled {
		t.Fatal("the key is still there")
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool {
		return e.Verb == verbMFARemove && strings.HasSuffix(e.Path, "?kind=webauthn")
	}) {
		t.Fatal("the trail does not say a key was removed")
	}
}

// An account can hold both: the second needs the first, and sign-in then offers
// the key first and the app after it.
func TestAKeyBesideAnApp(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)
	secret, _ := giveFactor(t, env, user)
	key := newVirtualKey(t)

	rec := env.doFrom(t, testOrigin, http.MethodPost, mfaEnrollPath, token, map[string]string{"kind": "webauthn"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a second factor without a proof of the first: %d", rec.Code)
	}
	rec = env.doFrom(t, testOrigin, http.MethodPost, mfaEnrollPath, token, map[string]string{
		"kind": "webauthn", "current_code": codeAt(t, secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body.String())
	}
	enrol := decode[mfaEnrollResponse](t, rec)
	rec = env.doFrom(t, testOrigin, http.MethodPost, mfaConfirmPath, token, map[string]any{
		"factor_id": enrol.FactorID, "credential": key.register(enrol.WebAuthn.Response.Challenge),
	})
	if rec.Code != http.StatusOK || len(decode[mfaConfirmResponse](t, rec).RecoveryCodes) != 0 {
		t.Fatalf("a second factor replaced the recovery codes, or failed: %d %s", rec.Code, rec.Body.String())
	}

	body := passwordStep(t, env, "dev", "pw")
	if !slices.Equal(body.Methods, []string{"webauthn", "totp", "recovery"}) {
		t.Fatalf("methods: %v", body.Methods)
	}
	// The app still works on its own.
	if rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": body.Challenge, "code": codeAt(t, secret, 1),
	}); rec.Code != http.StatusOK {
		t.Fatalf("the app beside a key: %d %s", rec.Code, rec.Body.String())
	}
}

// An account that must enrol at sign-in may choose a key, and the enrolment
// challenge says it can.
func TestEnrolmentAtSignInCanBeAKey(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("root", "pw", db.RoleAdmin)
	setEnforcement(env, mfaEnforcementAdmins)
	key := newVirtualKey(t)

	body := passwordStep(t, env, "root", "pw")
	if !slices.Equal(body.Methods, []string{"totp", "webauthn"}) {
		t.Fatalf("enrolment methods: %v", body.Methods)
	}
	rec := env.doFrom(t, testOrigin, http.MethodPost, mfaChallengeEnroll, "", map[string]string{
		"challenge": body.Challenge, "kind": "webauthn",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body.String())
	}
	enrol := decode[mfaEnrollResponse](t, rec)
	rec = env.doFrom(t, testOrigin, http.MethodPost, mfaChallengeConfirm, "", map[string]any{
		"challenge": body.Challenge, "factor_id": enrol.FactorID,
		"credential": key.register(enrol.WebAuthn.Response.Challenge),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	confirmed := decode[mfaConfirmResponse](t, rec)
	if confirmed.Session == nil || !sessionClaims(t, env, confirmed.Session.Token).MFA {
		t.Fatalf("no session after enrolling a key: %+v", confirmed)
	}
}

// A key's answer for a registration that was made for another origin is not a
// key this console can use.
func TestARegistrationForAnotherOriginIsRefused(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)
	key := newVirtualKey(t)
	key.origin = "https://elsewhere.example.org"

	enrol := decode[mfaEnrollResponse](t, env.doFrom(t, testOrigin, http.MethodPost, mfaEnrollPath, token,
		map[string]string{"kind": "webauthn"}))
	rec := env.doFrom(t, testOrigin, http.MethodPost, mfaConfirmPath, token, map[string]any{
		"factor_id": enrol.FactorID, "credential": key.register(enrol.WebAuthn.Response.Challenge),
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a credential made for another origin: %d %s", rec.Code, rec.Body.String())
	}
	if decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil)).Enrolled {
		t.Fatal("the refused key was confirmed anyway")
	}
}
