package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const (
	loginPath           = "/api/v1/auth/login"
	mfaVerifyPath       = "/api/v1/auth/mfa/verify"
	mfaPath             = "/api/v1/auth/mfa"
	mfaEnrollPath       = "/api/v1/auth/mfa/enroll"
	mfaConfirmPath      = "/api/v1/auth/mfa/enroll/confirm"
	mfaChallengeEnroll  = "/api/v1/auth/mfa/challenge/enroll"
	mfaChallengeConfirm = "/api/v1/auth/mfa/challenge/enroll/confirm"
	mfaRecoveryPath     = "/api/v1/auth/mfa/recovery-codes"
)

// giveFactor puts a confirmed TOTP factor and a recovery set on an account
// directly, as an enrolment that already happened. The factor's last accepted
// step is well in the past, so a code for now is fresh.
func giveFactor(t *testing.T, env *testEnv, user *db.User) (secret string, codes []string) {
	t.Helper()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	factor := &db.MFAFactor{UserID: user.ID, Kind: db.MFAKindTOTP, Name: "phone", Secret: secret}
	if err := env.store.CreatePendingMFAFactor(ctx, factor); err != nil {
		t.Fatal(err)
	}
	codes, err = auth.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hashes[i] = auth.HashRecoveryCode(code)
	}
	past := auth.TOTPStep(time.Now()) - 10
	if err := env.store.ConfirmMFAFactor(ctx, user.ID, factor.ID, past, time.Now(), hashes); err != nil {
		t.Fatal(err)
	}
	return secret, codes
}

// codeAt is the code a secret produces offset steps from now. A test that
// proves a factor twice uses two offsets: the same step is, by design, good once.
func codeAt(t *testing.T, secret string, offset int64) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, auth.TOTPStep(time.Now())+offset)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func setEnforcement(env *testEnv, policy string) {
	env.store.settings[db.SettingMFAEnforcement] = policy
}

type challengeBody struct {
	Token                 string   `json:"token"`
	MFARequired           bool     `json:"mfa_required"`
	MFAEnrollmentRequired bool     `json:"mfa_enrollment_required"`
	Challenge             string   `json:"challenge"`
	Methods               []string `json:"methods"`
}

func passwordStep(t *testing.T, env *testEnv, username, password string) challengeBody {
	t.Helper()
	rec := env.do(t, http.MethodPost, loginPath, "", map[string]string{"username": username, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("password step: %d %s", rec.Code, rec.Body.String())
	}
	return decode[challengeBody](t, rec)
}

func sessionClaims(t *testing.T, env *testEnv, token string) *auth.Claims {
	t.Helper()
	claims, err := env.jwt.Parse(token)
	if err != nil {
		t.Fatalf("session does not parse: %v", err)
	}
	return claims
}

func TestSignInWithoutAFactorIsUnchanged(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("dev", "pw", db.RoleUser)

	body := passwordStep(t, env, "dev", "pw")
	if body.Token == "" || body.MFARequired || body.Challenge != "" {
		t.Fatalf("expected a plain session: %+v", body)
	}
	if sessionClaims(t, env, body.Token).MFA {
		t.Fatal("a password-only session claims a second factor")
	}
}

func TestAFactorTurnsAPasswordIntoAChallengeNotASession(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)

	body := passwordStep(t, env, "dev", "pw")
	if body.Token != "" {
		t.Fatal("a right password alone was answered with a session")
	}
	if !body.MFARequired || !strings.HasPrefix(body.Challenge, mfaChallengePrefix) {
		t.Fatalf("expected a verify challenge: %+v", body)
	}
	if !slices.Contains(body.Methods, "recovery") {
		t.Fatalf("recovery codes are held but not offered: %v", body.Methods)
	}

	rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": body.Challenge, "code": codeAt(t, secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	session := decode[loginResponse](t, rec)
	if !sessionClaims(t, env, session.Token).MFA {
		t.Fatal("the session does not record its second factor")
	}

	// The challenge is spent: proving the factor again with it is refused.
	rec = env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": body.Challenge, "code": codeAt(t, secret, 1),
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a spent challenge opened a session: %d", rec.Code)
	}
}

func TestAWrongCodeLeavesTheChallengeStanding(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)
	body := passwordStep(t, env, "dev", "pw")

	wrong := "000000"
	if wrong == codeAt(t, secret, 0) || wrong == codeAt(t, secret, -1) || wrong == codeAt(t, secret, 1) {
		wrong = "999999"
	}
	rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": wrong})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong code: %d", rec.Code)
	}
	rec = env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": body.Challenge, "code": codeAt(t, secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the right code after a wrong one: %d %s", rec.Code, rec.Body.String())
	}
}

// A code is good once, across sign-ins: the second challenge is fresh, the code
// is not.
func TestATOTPCodeIsNotReplayable(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)
	code := codeAt(t, secret, 0)

	first := passwordStep(t, env, "dev", "pw")
	if rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": first.Challenge, "code": code,
	}); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	second := passwordStep(t, env, "dev", "pw")
	if rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": second.Challenge, "code": code,
	}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a replayed code opened a second session: %d", rec.Code)
	}
}

func TestTheCodeStepIsThrottledLikeAPassword(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)
	body := passwordStep(t, env, "dev", "pw")

	wrong := "000000"
	for offset := int64(-1); offset <= 1; offset++ {
		if wrong == codeAt(t, secret, offset) {
			wrong = "999999"
		}
	}
	for range 20 {
		rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": wrong})
		if rec.Code == http.StatusTooManyRequests {
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("a lock without Retry-After")
			}
			return
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unexpected %d", rec.Code)
		}
	}
	t.Fatal("twenty wrong codes and no lock")
}

func TestAnEnrolChallengeIsNotAVerifyChallenge(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("root", "pw", db.RoleAdmin)
	setEnforcement(env, mfaEnforcementAdmins)

	body := passwordStep(t, env, "root", "pw")
	if !body.MFAEnrollmentRequired || body.Token != "" {
		t.Fatalf("expected an enrolment challenge and no session: %+v", body)
	}
	rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": "123456"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an enrolment challenge passed the verify step: %d", rec.Code)
	}
}

func TestEnforcementCoversOnlyWhomItNames(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("root", "pw", db.RoleAdmin)
	env.store.addUser("dev", "pw", db.RoleUser)

	setEnforcement(env, mfaEnforcementAdmins)
	if body := passwordStep(t, env, "dev", "pw"); body.Token == "" {
		t.Fatal("admins-only enforcement held up a user")
	}
	if body := passwordStep(t, env, "root", "pw"); !body.MFAEnrollmentRequired {
		t.Fatal("admins-only enforcement let an unenrolled admin in")
	}

	setEnforcement(env, mfaEnforcementAll)
	if body := passwordStep(t, env, "dev", "pw"); !body.MFAEnrollmentRequired {
		t.Fatal("enforcement for all let an unenrolled user in")
	}

	// A stored value the server does not know reads as off, never as a lock.
	setEnforcement(env, "everyone-please")
	if body := passwordStep(t, env, "root", "pw"); body.Token == "" {
		t.Fatal("an unknown policy value locked an admin out")
	}
}

// The path an account that must have a factor walks: password, enrolment
// challenge, secret, first code — and only then a session, which records it.
func TestEnrolmentAtSignInEndsInASession(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	env.store.addUser("root", "pw", db.RoleAdmin)
	setEnforcement(env, mfaEnforcementAdmins)

	challenge := passwordStep(t, env, "root", "pw").Challenge
	rec := env.do(t, http.MethodPost, mfaChallengeEnroll, "", map[string]string{"challenge": challenge, "name": "phone"})
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol: %d %s", rec.Code, rec.Body.String())
	}
	enrol := decode[mfaEnrollResponse](t, rec)
	if enrol.Secret == "" || !strings.HasPrefix(enrol.QR, "data:image/png;base64,") ||
		!strings.Contains(enrol.URI, "root@kubemg.example.com") {
		t.Fatalf("enrolment: %+v", enrol)
	}

	rec = env.do(t, http.MethodPost, mfaChallengeConfirm, "", map[string]any{
		"challenge": challenge, "factor_id": enrol.FactorID, "code": codeAt(t, enrol.Secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	confirmed := decode[mfaConfirmResponse](t, rec)
	if len(confirmed.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Fatalf("the first factor came without recovery codes: %+v", confirmed)
	}
	if confirmed.Session == nil || !sessionClaims(t, env, confirmed.Session.Token).MFA {
		t.Fatalf("no session, or one that does not record the factor: %+v", confirmed.Session)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool { return e.Verb == verbMFAEnroll }) {
		t.Fatal("the enrolment is not in the trail")
	}

	// The challenge is spent; the next sign-in owes the factor it just enrolled.
	if rec := env.do(t, http.MethodPost, mfaChallengeEnroll, "", map[string]string{"challenge": challenge}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a spent enrolment challenge was accepted again: %d", rec.Code)
	}
	if body := passwordStep(t, env, "root", "pw"); !body.MFARequired {
		t.Fatalf("after enrolling, sign-in did not ask for the factor: %+v", body)
	}
}

func TestAWrongFirstCodeDoesNotConfirm(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)

	enrol := decode[mfaEnrollResponse](t, env.do(t, http.MethodPost, mfaEnrollPath, token, map[string]string{}))
	wrong := "000000"
	for offset := int64(-1); offset <= 1; offset++ {
		if wrong == codeAt(t, enrol.Secret, offset) {
			wrong = "999999"
		}
	}
	rec := env.do(t, http.MethodPost, mfaConfirmPath, token, map[string]any{"factor_id": enrol.FactorID, "code": wrong})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong first code: %d", rec.Code)
	}
	status := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil))
	if status.Enrolled {
		t.Fatal("a factor confirmed by a wrong code")
	}
}

// A stolen session must not be able to add the thief's authenticator.
func TestASecondFactorNeedsACodeFromTheFirst(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost, mfaEnrollPath, token, map[string]string{"name": "laptop"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a second factor without a code: %d", rec.Code)
	}
	rec = env.do(t, http.MethodPost, mfaEnrollPath, token, map[string]string{
		"name": "laptop", "current_code": codeAt(t, secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("a second factor with a code: %d %s", rec.Code, rec.Body.String())
	}
	enrol := decode[mfaEnrollResponse](t, rec)
	rec = env.do(t, http.MethodPost, mfaConfirmPath, token, map[string]any{
		"factor_id": enrol.FactorID, "code": codeAt(t, enrol.Secret, 0),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm second: %d %s", rec.Code, rec.Body.String())
	}
	if decode[mfaConfirmResponse](t, rec).RecoveryCodes != nil {
		t.Fatal("a second factor replaced the recovery codes")
	}
}

func TestARecoveryCodeSignsInOnceAndIsRecorded(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("dev", "pw", db.RoleUser)
	_, codes := giveFactor(t, env, user)

	body := passwordStep(t, env, "dev", "pw")
	rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{
		"challenge": body.Challenge, "code": strings.ToUpper(codes[0]),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery code: %d %s", rec.Code, rec.Body.String())
	}
	session := decode[mfaSessionResponse](t, rec)
	if session.RecoveryCodesRemaining == nil || *session.RecoveryCodesRemaining != auth.RecoveryCodeCount-1 {
		t.Fatalf("remaining: %v", session.RecoveryCodesRemaining)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool { return e.Verb == verbMFARecoveryUsed }) {
		t.Fatal("a spent recovery code is not in the trail")
	}

	body = passwordStep(t, env, "dev", "pw")
	rec = env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": codes[0]})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a spent recovery code worked twice: %d", rec.Code)
	}
}

func TestRegeneratingRecoveryCodesRetiresTheOldSet(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, old := giveFactor(t, env, user)
	token := env.tokenFor(t, user)

	if rec := env.do(t, http.MethodPost, mfaRecoveryPath, token, map[string]string{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("regenerate without a code: %d", rec.Code)
	}
	rec := env.do(t, http.MethodPost, mfaRecoveryPath, token, map[string]string{"code": codeAt(t, secret, 0)})
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate: %d %s", rec.Code, rec.Body.String())
	}
	fresh := decode[recoveryCodesResponse](t, rec).RecoveryCodes
	if len(fresh) != auth.RecoveryCodeCount {
		t.Fatalf("got %d codes", len(fresh))
	}

	body := passwordStep(t, env, "dev", "pw")
	if rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": old[1]}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a code from the retired set still worked: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, mfaVerifyPath, "", map[string]string{"challenge": body.Challenge, "code": fresh[0]}); rec.Code != http.StatusOK {
		t.Fatalf("a code from the new set: %d", rec.Code)
	}
}

func TestRemovingAFactor(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("dev", "pw", db.RoleUser)
	secret, _ := giveFactor(t, env, user)
	token := env.tokenFor(t, user)
	factorID := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, token, nil)).Factors[0].ID
	path := "/api/v1/auth/mfa/factors/" + itoa(factorID)

	if rec := env.do(t, http.MethodDelete, path, token, map[string]string{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("removal without a code: %d", rec.Code)
	}

	setEnforcement(env, mfaEnforcementAll)
	if rec := env.do(t, http.MethodDelete, path, token, map[string]string{"code": codeAt(t, secret, 0)}); rec.Code != http.StatusConflict {
		t.Fatalf("the only factor went while the account must have one: %d", rec.Code)
	}

	setEnforcement(env, mfaEnforcementOff)
	rec := env.do(t, http.MethodDelete, path, token, map[string]string{"code": codeAt(t, secret, 1)})
	if rec.Code != http.StatusOK {
		t.Fatalf("removal: %d %s", rec.Code, rec.Body.String())
	}
	status := decode[mfaStatusResponse](t, rec)
	if status.Enrolled || status.RecoveryCodesRemaining != 0 {
		t.Fatalf("after removing the last factor: %+v", status)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool { return e.Verb == verbMFARemove }) {
		t.Fatal("the removal is not in the trail")
	}
	if body := passwordStep(t, env, "dev", "pw"); body.Token == "" {
		t.Fatal("a password-only sign-in after removing the factor did not open a session")
	}
}

func TestResettingSomebodyElsesFactor(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	root := env.store.addSuperAdmin("root", "pw")
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	giveFactor(t, env, admin)
	giveFactor(t, env, root)
	giveFactor(t, env, dev)
	adminToken := env.tokenFor(t, admin)
	reset := func(target *db.User) string { return "/api/v1/users/" + itoa(target.ID) + "/mfa/reset" }

	if rec := env.do(t, http.MethodPost, reset(admin), adminToken, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an admin reset their own factor: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, reset(root), adminToken, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an admin reset a super admin: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, reset(dev), env.tokenFor(t, dev), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a user reached the reset: %d", rec.Code)
	}

	rec := env.do(t, http.MethodPost, reset(dev), adminToken, nil)
	if rec.Code != http.StatusOK || decode[mfaResetResponse](t, rec).FactorsRemoved != 1 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	events := auditor.all()
	if !slices.ContainsFunc(events, func(e bastion.Event) bool {
		return e.Verb == verbMFAReset && e.Username == "admin" && e.ImpersonatedUser == "dev"
	}) {
		t.Fatalf("the reset is not in the trail with both identities: %+v", events)
	}
	if body := passwordStep(t, env, "dev", "pw"); body.Token == "" {
		t.Fatal("after a reset the account still owes a factor it no longer has")
	}

	// A super admin may reset another super admin.
	other := env.store.addSuperAdmin("root2", "pw")
	if rec := env.do(t, http.MethodPost, reset(root), env.tokenFor(t, other), nil); rec.Code != http.StatusOK {
		t.Fatalf("a super admin resetting a super admin: %d", rec.Code)
	}
}

func TestFactorsBelongToPasswordAccountsOnly(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	oidc := env.store.addUser("federated", "pw", db.RoleUser)
	oidc.AuthSource = db.ProtocolOIDC
	ldap := env.store.addUser("directory", "pw", db.RoleUser)
	ldap.AuthSource = db.ProtocolLDAP
	machine := env.store.addUser("ci", "pw", db.RoleUser)
	machine.AccountType = db.AccountTypeMachine

	status := decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, env.tokenFor(t, oidc), nil))
	if status.Applies {
		t.Fatal("an OIDC account was offered KubeMG's factor")
	}
	if rec := env.do(t, http.MethodPost, mfaEnrollPath, env.tokenFor(t, oidc), map[string]string{}); rec.Code != http.StatusConflict {
		t.Fatalf("an OIDC account enrolled: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/v1/users/"+itoa(oidc.ID)+"/mfa/reset", env.tokenFor(t, admin), nil); rec.Code != http.StatusConflict {
		t.Fatalf("an OIDC account was reset: %d", rec.Code)
	}

	// A directory bind is only a password, so an LDAP account is covered.
	if !decode[mfaStatusResponse](t, env.do(t, http.MethodGet, mfaPath, env.tokenFor(t, ldap), nil)).Applies {
		t.Fatal("an LDAP account was not offered KubeMG's factor")
	}
	if rec := env.do(t, http.MethodPost, mfaEnrollPath, env.tokenFor(t, ldap), map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("an LDAP account could not enrol: %d %s", rec.Code, rec.Body.String())
	}

	setEnforcement(env, mfaEnforcementAll)
	if (&db.User{AccountType: db.AccountTypeMachine}).MFAApplies() || mfaRequiredFor(mfaEnforcementAll, machine) {
		t.Fatal("a machine account was made to hold a factor")
	}
}

// A factor state that cannot be read refuses the sign-in rather than letting a
// password through on its own.
func TestAnUnreadableFactorStateRefusesSignIn(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("dev", "pw", db.RoleUser)
	env.store.mfaErr = errors.New("database unavailable")

	rec := env.do(t, http.MethodPost, loginPath, "", map[string]string{"username": "dev", "password": "pw"})
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("expected a refusal, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestEnforcementIsValidatedOnSave(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	if rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{"mfa_enforcement": "sometimes"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown policy was stored: %d", rec.Code)
	}
	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{"mfa_enforcement": "Admins"})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode[settingsResponse](t, rec).Effective.MFAEnforcement; got != mfaEnforcementAdmins {
		t.Fatalf("effective policy %q", got)
	}
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{"mfa_enforcement": ""})
	if got := decode[settingsResponse](t, rec).Effective.MFAEnforcement; got != mfaEnforcementOff {
		t.Fatalf("a cleared policy reads %q", got)
	}
}

func TestPostureSaysWhoSignsInWithAPasswordAlone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addSuperAdmin("root", "pw")
	token := env.tokenFor(t, admin)

	checks := decode[deploymentPostureResponse](t, env.do(t, http.MethodGet, "/api/v1/settings/deployment", token, nil)).Checks
	severity := map[string]string{}
	for _, check := range checks {
		severity[check.Key] = check.Severity
	}
	if severity["mfa"] != checkWarn {
		t.Fatalf("an optional factor did not warn: %+v", checks)
	}
	if severity["mfa-super-admins"] != checkWarn {
		t.Fatalf("a super admin without a factor was not named: %+v", checks)
	}

	setEnforcement(env, mfaEnforcementAdmins)
	root, _ := env.store.UserByUsername(context.Background(), "root")
	giveFactor(t, env, root)
	checks = decode[deploymentPostureResponse](t, env.do(t, http.MethodGet, "/api/v1/settings/deployment", token, nil)).Checks
	for _, check := range checks {
		if check.Key == "mfa" && check.Severity != checkOK {
			t.Fatalf("enforcement for admins still warns: %+v", check)
		}
		if check.Key == "mfa-super-admins" {
			t.Fatalf("an enrolled super admin is still counted: %+v", check)
		}
	}
}

func TestBreakGlassResetsAtBootAndSaysSo(t *testing.T) {
	env := newTestEnv(t)
	root := env.store.addSuperAdmin("root", "pw")
	giveFactor(t, env, root)
	auditor := &recordingAuditor{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ResetMFAAtBoot(context.Background(), env.store, auditor, " root ", logger)
	if state, _ := env.store.MFAState(context.Background(), root.ID); state.Enrolled() {
		t.Fatal("the break-glass left the factor in place")
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("the reset was not loud: %s", logs.String())
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool {
		return e.Verb == verbMFAReset && e.Username == BootUsername && e.ImpersonatedUser == "root"
	}) {
		t.Fatal("the boot reset is not in the trail")
	}

	logs.Reset()
	ResetMFAAtBoot(context.Background(), env.store, auditor, "nobody", logger)
	if !strings.Contains(logs.String(), "names no account") {
		t.Fatalf("an unknown name was not reported: %s", logs.String())
	}
}
