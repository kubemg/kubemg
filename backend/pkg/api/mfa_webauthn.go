package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// webauthnTimeout is how long the browser holds a security-key prompt open, and
// how long the server will accept the answer to one. It is shorter than the
// sign-in challenge it sits inside, so a prompt that timed out can be asked for
// again without typing the password a second time.
const webauthnTimeout = 2 * time.Minute

// webauthnStatus says whether a security key can be used on this install, and
// if not, why — in words the person reading the profile can act on.
type webauthnStatus struct {
	Available bool `json:"available"`
	// RPID is the host every key is bound to: the public URL's.
	RPID   string `json:"rp_id,omitempty"`
	Reason string `json:"reason,omitempty"`
	// origin is the one page a key answers to.
	origin string
}

// relyingParty is the WebAuthn relying party this install is, derived from the
// public URL on every call because the public URL is a runtime setting.
//
// The host is the relying party's id, and the browser binds every key to it:
// a key registered at kubemg.example.com never answers anywhere else. That is
// the phishing resistance a security key exists for, and it is why a changed
// public URL leaves the keys registered under the old one listed but unusable.
func relyingParty(publicURL string) (*webauthn.WebAuthn, webauthnStatus) {
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || parsed.Hostname() == "" {
		return nil, webauthnStatus{Reason: "Security keys are bound to the console's public URL, and none is set."}
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case net.ParseIP(host) != nil:
		return nil, webauthnStatus{Reason: "Security keys need the console to be reached by a domain name, and its public URL is an IP address."}
	case parsed.Scheme != "https" && host != "localhost":
		return nil, webauthnStatus{Reason: "Security keys need the console on https, and its public URL is plain http."}
	}
	origin := parsed.Scheme + "://" + strings.ToLower(parsed.Host)
	timeouts := webauthn.TimeoutConfig{Enforce: true, Timeout: webauthnTimeout, TimeoutUVD: webauthnTimeout}
	rp, err := webauthn.New(&webauthn.Config{
		RPID:          host,
		RPDisplayName: mfaIssuer,
		RPOrigins:     []string{origin},
		// The make and model of a key are nothing this console decides on, and
		// asking for them makes some authenticators prompt for consent.
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// A second factor, not a passwordless sign-in: the key need not hold
			// an account, and the password already proved who is signing in.
			ResidentKey:      protocol.ResidentKeyRequirementDiscouraged,
			UserVerification: protocol.VerificationDiscouraged,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: timeouts, Registration: timeouts},
	})
	if err != nil {
		return nil, webauthnStatus{Reason: "Security keys cannot be used with this public URL: " + err.Error()}
	}
	return rp, webauthnStatus{Available: true, RPID: host, origin: origin}
}

func (s *server) relyingParty(ctx context.Context) (*webauthn.WebAuthn, webauthnStatus) {
	return relyingParty(s.settings(ctx).PublicURL)
}

// refuseForeignOrigin answers a ceremony started from a page other than the
// public URL's, and reports whether it did. The browser would refuse it anyway
// — a key answers only to the host it is bound to — but with an error that says
// nothing about why. A request without an Origin is not a browser's, and is
// left to the signature check.
func refuseForeignOrigin(c *gin.Context, status webauthnStatus) bool {
	origin := strings.ToLower(strings.TrimRight(c.GetHeader("Origin"), "/"))
	if origin == "" || origin == status.origin {
		return false
	}
	c.JSON(http.StatusConflict, gin.H{
		"error": fmt.Sprintf("security keys answer to %s, and this page was opened at %s — sign in there to use one",
			status.origin, origin),
	})
	return true
}

// webauthnUser is an account as the WebAuthn library sees it: a stable handle,
// a name for the authenticator's own display, and the keys registered for the
// relying party it is being asked about.
type webauthnUser struct {
	user        *db.User
	credentials []webauthn.Credential
	factorIDs   []uint
}

func newWebAuthnUser(user *db.User, factors []db.MFAFactor, rpID string, logger *slog.Logger) webauthnUser {
	out := webauthnUser{user: user}
	for _, factor := range factors {
		if factor.Kind != db.MFAKindWebAuthn || factor.RPID != rpID {
			continue
		}
		var credential webauthn.Credential
		if err := json.Unmarshal([]byte(factor.Secret), &credential); err != nil {
			logger.Error("a security key's stored credential does not decode; it cannot sign in",
				slog.Uint64("user_id", uint64(user.ID)), slog.Uint64("factor_id", uint64(factor.ID)),
				slog.String("error", err.Error()))
			continue
		}
		// The row's counter, not the one in the record: the row is what the
		// conditional write advances.
		credential.Authenticator.SignCount = uint32(min(max(factor.LastStep, 0), math.MaxUint32))
		out.credentials = append(out.credentials, credential)
		out.factorIDs = append(out.factorIDs, factor.ID)
	}
	return out
}

// WebAuthnID is the account's user handle: its id, which is stable for the
// account's life and says nothing about the person.
func (u webauthnUser) WebAuthnID() []byte {
	handle := make([]byte, 8)
	binary.BigEndian.PutUint64(handle, uint64(u.user.ID))
	return handle
}

func (u webauthnUser) WebAuthnName() string                       { return u.user.Username }
func (u webauthnUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (u webauthnUser) factorFor(credentialID []byte) (uint, bool) {
	for i, credential := range u.credentials {
		if bytes.Equal(credential.ID, credentialID) {
			return u.factorIDs[i], true
		}
	}
	return 0, false
}

// usableKeys counts the security keys an account can sign in with here: those
// registered for the relying party the public URL makes this install today.
func usableKeys(factors []db.MFAFactor, status webauthnStatus) int {
	if !status.Available {
		return 0
	}
	count := 0
	for _, factor := range factors {
		if factor.Kind == db.MFAKindWebAuthn && factor.RPID == status.RPID {
			count++
		}
	}
	return count
}

// mfaMethods is what the code step of a sign-in accepts, in the order the form
// offers them: a security key first, because it cannot be phished, then an
// authenticator app, then a recovery code.
func mfaMethods(state db.MFAState, status webauthnStatus) []string {
	methods := []string{}
	if usableKeys(state.Factors, status) > 0 {
		methods = append(methods, db.MFAKindWebAuthn)
	}
	for _, factor := range state.Factors {
		if factor.Kind == db.MFAKindTOTP {
			methods = append(methods, db.MFAKindTOTP)
			break
		}
	}
	if state.RecoveryCodesRemaining > 0 {
		methods = append(methods, proofRecovery)
	}
	return methods
}

// webauthnProof is a security key's answer to a ceremony: the ceremony it
// answers and the browser's PublicKeyCredential, as JSON.
type webauthnProof struct {
	Ceremony   string          `json:"ceremony"`
	Credential json.RawMessage `json:"credential"`
}

// factorProof is what a request offers as proof of a factor: a code, or a
// security key's answer.
type factorProof struct {
	Code     string
	WebAuthn *webauthnProof
}

func (p factorProof) empty() bool {
	return strings.TrimSpace(p.Code) == "" && p.WebAuthn == nil
}

// refusal is what a proof that proved nothing is answered with.
func (p factorProof) refusal() string {
	if p.WebAuthn != nil {
		return "that security key's answer was not accepted"
	}
	return "that code is not right"
}

// proveWebAuthn checks a security key's answer. The ceremony is spent whatever
// the answer — a WebAuthn challenge is good for one signature — and accepting
// it advances the key's counter by a conditional write, so a counter that did
// not move forward, the sign of a cloned key, is refused.
func (s *server) proveWebAuthn(ctx context.Context, user *db.User, state db.MFAState, proof *webauthnProof) (bool, error) {
	if strings.TrimSpace(proof.Ceremony) == "" || len(proof.Credential) == 0 {
		return false, nil
	}
	row, err := s.store.TakeMFACeremony(ctx, hashMFAChallenge(proof.Ceremony))
	if err != nil {
		return false, err
	}
	if row == nil || row.UserID != user.ID {
		return false, nil
	}
	rp, status := s.relyingParty(ctx)
	if !status.Available {
		return false, nil
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(row.Ceremony), &session); err != nil {
		return false, fmt.Errorf("decode webauthn ceremony: %w", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(proof.Credential)
	if err != nil {
		return false, nil
	}
	subject := newWebAuthnUser(user, state.Factors, status.RPID, s.log())
	credential, err := rp.ValidateLogin(subject, session, parsed)
	if err != nil {
		return false, nil
	}
	factorID, ok := subject.factorFor(credential.ID)
	if !ok {
		return false, nil
	}
	counter := int64(parsed.Response.AuthenticatorData.Counter)
	if credential.Authenticator.CloneWarning {
		s.log().Warn("a security key signed with a counter that did not move forward; refused as a possible clone",
			slog.Uint64("user_id", uint64(user.ID)), slog.Uint64("factor_id", uint64(factorID)),
			slog.Int64("counter", counter))
		return false, nil
	}
	return s.store.AdvanceMFACounter(ctx, factorID, counter, time.Now().UTC())
}

type webauthnAssertRequest struct {
	// Challenge is the sign-in's, when the key is the code step of one. Absent,
	// the session is what authorises the ceremony — a change to the factors
	// proved with a key rather than a code.
	Challenge string `json:"challenge"`
}

type webauthnAssertResponse struct {
	Ceremony  string                        `json:"ceremony"`
	ExpiresAt time.Time                     `json:"expires_at"`
	Options   *protocol.CredentialAssertion `json:"options"`
}

// beginWebAuthnAssertion opens a ceremony for a security key to sign: the
// challenge, and the keys the account may answer with. It proves nothing on its
// own — the answer goes to whichever route the key is proving a factor for.
func (s *server) beginWebAuthnAssertion(c *gin.Context) {
	var req webauthnAssertRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}
	var user *db.User
	ok := false
	if strings.TrimSpace(req.Challenge) != "" {
		user, _, ok = s.challengeUser(c, req.Challenge, db.MFAChallengeVerify)
	} else if _, signedIn := auth.ClaimsFrom(c); signedIn {
		user, ok = s.currentUser(c)
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
	}
	if !ok || refuseMFAFor(c, user) {
		return
	}
	rp, status := s.relyingParty(c.Request.Context())
	if !status.Available {
		c.JSON(http.StatusConflict, gin.H{"error": status.Reason})
		return
	}
	if refuseForeignOrigin(c, status) {
		return
	}
	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	subject := newWebAuthnUser(user, state.Factors, status.RPID, s.log())
	if len(subject.credentials) == 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": "no security key is registered to this account for " + status.RPID,
		})
		return
	}
	options, session, err := rp.BeginLogin(subject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the security key check"})
		return
	}
	ceremony, err := json.Marshal(session)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the security key check"})
		return
	}
	token, expiresAt, err := s.mintMFAChallenge(ctx, user.ID, db.MFAChallengeWebAuthn, string(ceremony))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the security key check"})
		return
	}
	c.JSON(http.StatusOK, webauthnAssertResponse{Ceremony: token, ExpiresAt: expiresAt, Options: options})
}

// beginWebAuthnEnrollment registers a key in two halves, like an app: this one
// asks the browser to make a credential and files the ceremony on a pending
// factor; confirmMFA checks what the key made and confirms it.
func (s *server) beginWebAuthnEnrollment(
	c *gin.Context, rp *webauthn.WebAuthn, status webauthnStatus, user *db.User, state db.MFAState, name string,
) {
	ctx := c.Request.Context()
	subject := newWebAuthnUser(user, state.Factors, status.RPID, s.log())
	// The keys already registered are excluded, so the browser says "already
	// registered" rather than making a second credential on the same key.
	options, session, err := rp.BeginRegistration(subject,
		webauthn.WithExclusions(webauthn.Credentials(subject.credentials).CredentialDescriptors()))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start registering the key"})
		return
	}
	ceremony, err := json.Marshal(session)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start registering the key"})
		return
	}
	factor := &db.MFAFactor{
		UserID: user.ID, Kind: db.MFAKindWebAuthn, Name: name, Secret: string(ceremony), RPID: status.RPID,
	}
	if err := s.store.CreatePendingMFAFactor(ctx, factor); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the factor"})
		return
	}
	c.JSON(http.StatusOK, mfaEnrollResponse{FactorID: factor.ID, Kind: db.MFAKindWebAuthn, WebAuthn: options})
}

// finishWebAuthnEnrollment checks the credential a key made against the
// pending factor's ceremony, and returns the credential record to store and its
// first counter. It answers the request itself when it does not.
func (s *server) finishWebAuthnEnrollment(
	c *gin.Context, user *db.User, state db.MFAState, factor *db.MFAFactor, answer json.RawMessage, attempt *auth.Attempt,
) (string, int64, bool) {
	ctx := c.Request.Context()
	rp, status := s.relyingParty(ctx)
	if !status.Available || status.RPID != factor.RPID {
		c.JSON(http.StatusConflict, gin.H{
			"error": "the console's public URL changed while the key was being registered; start again",
		})
		return "", 0, false
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(factor.Secret), &session); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "that enrolment is not in progress; start again"})
		return "", 0, false
	}
	refuse := func() (string, int64, bool) {
		if attempt != nil {
			s.signInFailed(c, attempt)
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "the key's answer was not accepted; try registering it again"})
		return "", 0, false
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(answer)
	if err != nil {
		return refuse()
	}
	subject := newWebAuthnUser(user, state.Factors, status.RPID, s.log())
	credential, err := rp.CreateCredential(subject, session, parsed)
	if err != nil {
		return refuse()
	}
	if _, taken := subject.factorFor(credential.ID); taken {
		c.JSON(http.StatusConflict, gin.H{"error": "that key is already registered to your account"})
		return "", 0, false
	}
	record, err := json.Marshal(credential)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store the key"})
		return "", 0, false
	}
	return string(record), int64(credential.Authenticator.SignCount), true
}

// kindQuery is the factor kind as the audit path's query, so the trail says
// whether an app or a key was added — cli-login's convention for a detail the
// path alone does not carry.
func kindQuery(kind string) string {
	if kind == "" {
		return ""
	}
	return "?kind=" + url.QueryEscape(kind)
}
