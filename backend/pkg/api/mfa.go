package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// What a second factor's life is called in the trail. Like password-change they
// are KubeMG acts and name no cluster. Reset, remove and a spent recovery code
// are on auditpolicy's floor: each one weakens how somebody authenticates, and
// a suppressible record of that is the first thing an auditor asks about.
const (
	verbMFAEnroll             = "mfa-enroll"
	verbMFARemove             = "mfa-remove"
	verbMFAReset              = "mfa-reset"
	verbMFARecoveryUsed       = "mfa-recovery-used"
	verbMFARecoveryRegenerate = "mfa-recovery-regenerate"
)

// The values mfa_enforcement takes. Anything else stored reads as off: a
// mistyped row must not lock every administrator out at their next sign-in.
const (
	mfaEnforcementOff    = "off"
	mfaEnforcementAdmins = "admins"
	mfaEnforcementAll    = "all"
)

var mfaEnforcementValues = []string{mfaEnforcementOff, mfaEnforcementAdmins, mfaEnforcementAll}

// mfaChallengeTTL is how long the code step of a sign-in may take. Five minutes
// covers finding the phone; a challenge left open longer is a half-signed-in
// session nobody is attending to.
const mfaChallengeTTL = 5 * time.Minute

// mfaChallengePrefix marks a sign-in challenge, so one pasted into the wrong
// place reads as what it is.
const mfaChallengePrefix = "kmgc_"

// mfaIssuer is what an authenticator app files the factor under.
const mfaIssuer = "KubeMG"

// maxFactorName bounds the label a person gives a factor.
const maxFactorName = 64

func normalizeMFAEnforcement(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, known := range mfaEnforcementValues {
		if value == known {
			return value
		}
	}
	return mfaEnforcementOff
}

// storedMFAEnforcement is the override as stored, "" when unset or unreadable.
func storedMFAEnforcement(stored map[string]string) string {
	value := strings.ToLower(strings.TrimSpace(stored[db.SettingMFAEnforcement]))
	if normalizeMFAEnforcement(value) != value {
		return ""
	}
	return value
}

// mfaRequiredFor reports whether the policy obliges an account to hold a
// factor. It never obliges an account KubeMG's factor does not govern.
func mfaRequiredFor(policy string, user *db.User) bool {
	if !user.MFAApplies() {
		return false
	}
	switch normalizeMFAEnforcement(policy) {
	case mfaEnforcementAll:
		return true
	case mfaEnforcementAdmins:
		return user.IsAdmin()
	}
	return false
}

// mfaChallengeResponse is what a right password is answered with when a
// second factor is still owed. It carries no token.
type mfaChallengeResponse struct {
	MFARequired           bool      `json:"mfa_required,omitempty"`
	MFAEnrollmentRequired bool      `json:"mfa_enrollment_required,omitempty"`
	Challenge             string    `json:"challenge"`
	ExpiresAt             time.Time `json:"challenge_expires_at"`
	// Methods is what the code step accepts, so the form can offer the
	// recovery-code path only when there is one.
	Methods []string `json:"methods,omitempty"`
}

// challengeSignIn decides whether a right password is enough. It answers the
// request itself and returns true when it is not: an account holding a factor
// gets a verify challenge, one that must hold a factor and has none gets an
// enrolment challenge, and in neither case a session.
//
// A factor state that cannot be read refuses the sign-in. Failing open here
// would turn a database blip into a password-only console for every account
// that went to the trouble of enrolling.
func (s *server) challengeSignIn(c *gin.Context, user *db.User) bool {
	if !user.MFAApplies() {
		return false
	}
	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not check the second factor"})
		return true
	}

	purpose := ""
	switch {
	case state.Enrolled():
		purpose = db.MFAChallengeVerify
	case mfaRequiredFor(s.settings(ctx).MFAEnforcement, user):
		purpose = db.MFAChallengeEnroll
	default:
		return false
	}

	challenge, expiresAt, err := s.mintMFAChallenge(ctx, user.ID, purpose)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the second step of sign-in"})
		return true
	}
	response := mfaChallengeResponse{Challenge: challenge, ExpiresAt: expiresAt}
	if purpose == db.MFAChallengeVerify {
		response.MFARequired = true
		response.Methods = []string{db.MFAKindTOTP}
		if state.RecoveryCodesRemaining > 0 {
			response.Methods = append(response.Methods, "recovery")
		}
	} else {
		response.MFAEnrollmentRequired = true
	}
	c.JSON(http.StatusOK, response)
	return true
}

func (s *server) mintMFAChallenge(ctx context.Context, userID uint, purpose string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("generate challenge: %w", err)
	}
	challenge := mfaChallengePrefix + base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := time.Now().UTC().Add(mfaChallengeTTL)
	err := s.store.PutMFAChallenge(ctx, &db.MFAChallenge{
		TokenHash: hashMFAChallenge(challenge), UserID: userID, Purpose: purpose, ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return challenge, expiresAt, nil
}

func hashMFAChallenge(challenge string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(challenge)))
	return hex.EncodeToString(sum[:])
}

// errChallengeGone is every way a challenge can fail to name a sign-in in
// progress. They are one answer on purpose: which of them it was is nothing the
// person can act on differently.
var errChallengeGone = errors.New("this sign-in has expired; sign in again")

// challengeUser resolves a challenge of the given purpose to the account it was
// minted for, without spending it.
func (s *server) challengeUser(c *gin.Context, challenge, purpose string) (*db.User, string, bool) {
	ctx := c.Request.Context()
	hash := hashMFAChallenge(challenge)
	row, err := s.store.MFAChallengeByHash(ctx, hash)
	if errors.Is(err, db.ErrNotFound) || (err == nil && row.Purpose != purpose) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": errChallengeGone.Error()})
		return nil, "", false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the sign-in"})
		return nil, "", false
	}
	user, err := s.store.UserByID(ctx, row.UserID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": errChallengeGone.Error()})
		return nil, "", false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the account"})
		return nil, "", false
	}
	// Disabled between the password and the code: the account is refused, as
	// it would have been one step earlier.
	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
		return nil, "", false
	}
	user.Normalize()
	return user, hash, true
}

// spendChallenge redeems a challenge once its factor has been proved. Losing the
// race to another request that proved it too is a refusal, never a second
// session.
func (s *server) spendChallenge(c *gin.Context, hash string) bool {
	taken, err := s.store.TakeMFAChallenge(c.Request.Context(), hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not finish the sign-in"})
		return false
	}
	if !taken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": errChallengeGone.Error()})
		return false
	}
	return true
}

// Ways a factor is proved.
const (
	proofTOTP     = "totp"
	proofRecovery = "recovery"
)

// proveFactor checks a submitted code against an account's confirmed factors,
// or failing the TOTP shape, against its unspent recovery codes. It returns how
// the factor was proved, or "" when the code proves nothing. Accepting a code
// is a write — a TOTP step advanced, a recovery code spent — so the same code
// cannot prove anything twice, on this replica or another.
func (s *server) proveFactor(ctx context.Context, user *db.User, state db.MFAState, code string) (string, error) {
	now := time.Now().UTC()
	if auth.LooksLikeTOTP(code) {
		for _, factor := range state.Factors {
			if factor.Kind != db.MFAKindTOTP {
				continue
			}
			step, ok := auth.VerifyTOTP(factor.Secret, code, factor.LastStep, now)
			if !ok {
				continue
			}
			advanced, err := s.store.AdvanceMFAStep(ctx, factor.ID, step, now)
			if err != nil {
				return "", err
			}
			if advanced {
				return proofTOTP, nil
			}
		}
		return "", nil
	}
	if strings.TrimSpace(code) == "" {
		return "", nil
	}
	used, err := s.store.UseMFARecoveryCode(ctx, user.ID, auth.HashRecoveryCode(code), now)
	if err != nil {
		return "", err
	}
	if used {
		return proofRecovery, nil
	}
	return "", nil
}

type mfaVerifyRequest struct {
	Challenge string `json:"challenge" binding:"required"`
	Code      string `json:"code" binding:"required"`
}

// mfaSessionResponse is a session finished by a second factor. When a recovery
// code did it, the number left is said so the person knows how close they are
// to a reset.
type mfaSessionResponse struct {
	loginResponse
	RecoveryCodesRemaining *int64 `json:"recovery_codes_remaining,omitempty"`
}

// verifyMFA is the code step of a sign-in. Throttled under the account, with
// the password routes' policy — one policy, not two: six digits are guessable
// in a way a password is not, and only the lock stands between them and a
// script.
func (s *server) verifyMFA(c *gin.Context) {
	var req mfaVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "challenge and code are required"})
		return
	}
	user, hash, ok := s.challengeUser(c, req.Challenge, db.MFAChallengeVerify)
	if !ok {
		return
	}
	attempt, ok := s.beginSignIn(c, mfaThrottleAccount(user.ID))
	if !ok {
		return
	}
	defer attempt.Release()

	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not check the second factor"})
		return
	}
	proof, err := s.proveFactor(ctx, user, state, req.Code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not check the second factor"})
		return
	}
	if proof == "" {
		s.signInFailed(c, attempt)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "that code is not right"})
		return
	}
	attempt.Succeed()
	if !s.spendChallenge(c, hash) {
		return
	}

	session, ok := s.newSession(c, user, true)
	if !ok {
		return
	}
	response := mfaSessionResponse{loginResponse: session}
	if proof == proofRecovery {
		s.recordMFAEvent(c, user, user, verbMFARecoveryUsed, "")
		if after, err := s.store.MFAState(ctx, user.ID); err == nil {
			response.RecoveryCodesRemaining = &after.RecoveryCodesRemaining
		}
	}
	c.JSON(http.StatusOK, response)
}

func mfaThrottleAccount(userID uint) string {
	return "mfa:" + strconv.FormatUint(uint64(userID), 10)
}

// mfaStatusResponse is an account's second factor as its owner sees it.
type mfaStatusResponse struct {
	// Applies is false for an account whose factor is somebody else's —
	// an OIDC or SAML provider's — or that has none by construction. The
	// console draws nothing for it.
	Applies                bool           `json:"applies"`
	Required               bool           `json:"required"`
	Enrolled               bool           `json:"enrolled"`
	Factors                []db.MFAFactor `json:"factors"`
	RecoveryCodesRemaining int64          `json:"recovery_codes_remaining"`
}

func (s *server) mfaStatus(ctx context.Context, user *db.User) (mfaStatusResponse, error) {
	out := mfaStatusResponse{Applies: user.MFAApplies(), Factors: []db.MFAFactor{}}
	if !out.Applies {
		return out, nil
	}
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		return out, err
	}
	out.Required = mfaRequiredFor(s.settings(ctx).MFAEnforcement, user)
	out.Enrolled = state.Enrolled()
	out.Factors = state.Factors
	out.RecoveryCodesRemaining = state.RecoveryCodesRemaining
	return out, nil
}

// getMFA reads the caller's own second factor.
func (s *server) getMFA(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	status, err := s.mfaStatus(c.Request.Context(), caller)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// refuseMFAFor answers for an account KubeMG's factor does not govern, in
// profile.go's words, and reports whether it did.
func refuseMFAFor(c *gin.Context, user *db.User) bool {
	switch {
	case user.IsMachine():
		c.JSON(http.StatusConflict, gin.H{
			"error": "a machine account has no password, so it has no second factor — it signs in with a revocable token",
		})
		return true
	case !user.MFAApplies():
		c.JSON(http.StatusConflict, gin.H{
			"error": "this account signs in through its identity provider, and its second factor lives there",
		})
		return true
	}
	return false
}

type mfaEnrollRequest struct {
	Name string `json:"name"`
	// Challenge is an enrolment challenge from a sign-in that could not finish
	// without a factor. Absent, the caller's session is what authorises this.
	Challenge string `json:"challenge"`
	// CurrentCode is required when the account already holds a factor: a
	// stolen session must not be able to add the thief's authenticator.
	CurrentCode string `json:"current_code"`
}

type mfaEnrollResponse struct {
	FactorID uint   `json:"factor_id"`
	Secret   string `json:"secret"`
	URI      string `json:"otpauth_uri"`
	QR       string `json:"qr"`
}

// enrollMFA starts an enrolment: a secret, shown once, waiting for its first
// code. Reached with a session, or — on the challenge route — with the
// enrolment challenge a sign-in was answered with.
func (s *server) enrollMFA(c *gin.Context) {
	var req mfaEnrollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}
	user, _, ok := s.mfaSubject(c, req.Challenge)
	if !ok {
		return
	}
	if refuseMFAFor(c, user) {
		return
	}
	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	if state.Enrolled() {
		if req.Challenge != "" {
			// An enrolment challenge is for an account with no factor; one that
			// gained a factor meanwhile signs in with it.
			c.JSON(http.StatusUnauthorized, gin.H{"error": errChallengeGone.Error()})
			return
		}
		if !s.requireCurrentFactor(c, user, state, req.CurrentCode,
			"adding another factor needs a code from one you already have") {
			return
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Authenticator app"
	}
	if len([]rune(name)) > maxFactorName {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("a factor's name is at most %d characters", maxFactorName),
		})
		return
	}

	secret, err := auth.NewTOTPSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the factor"})
		return
	}
	factor := &db.MFAFactor{UserID: user.ID, Kind: db.MFAKindTOTP, Name: name, Secret: secret}
	if err := s.store.CreatePendingMFAFactor(ctx, factor); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the factor"})
		return
	}
	uri := auth.TOTPURI(mfaIssuer, s.mfaAccountLabel(ctx, user), secret)
	qr, err := auth.TOTPQRCode(uri)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not draw the QR code"})
		return
	}
	c.JSON(http.StatusOK, mfaEnrollResponse{FactorID: factor.ID, Secret: secret, URI: uri, QR: qr})
}

// mfaAccountLabel is the account half of an authenticator entry: the username
// and the install it is for, because one phone holds production and staging.
func (s *server) mfaAccountLabel(ctx context.Context, user *db.User) string {
	if parsed, err := url.Parse(s.settings(ctx).PublicURL); err == nil && parsed.Hostname() != "" {
		return user.Username + "@" + parsed.Hostname()
	}
	return user.Username
}

type mfaConfirmRequest struct {
	FactorID  uint   `json:"factor_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
	Challenge string `json:"challenge"`
}

// mfaConfirmResponse carries the recovery codes when this was the account's
// first factor — shown once, never readable again — and a session when the
// enrolment was a sign-in's.
type mfaConfirmResponse struct {
	RecoveryCodes []string            `json:"recovery_codes,omitempty"`
	Session       *mfaSessionResponse `json:"session,omitempty"`
}

// confirmMFA finishes an enrolment with the first code the app produced.
func (s *server) confirmMFA(c *gin.Context) {
	var req mfaConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "factor_id and code are required"})
		return
	}
	user, hash, ok := s.mfaSubject(c, req.Challenge)
	if !ok {
		return
	}
	if refuseMFAFor(c, user) {
		return
	}
	// The challenge route is reachable without a session, so it is throttled
	// like the code step of a sign-in.
	var attempt *auth.Attempt
	if hash != "" {
		attempt, ok = s.beginSignIn(c, mfaThrottleAccount(user.ID))
		if !ok {
			return
		}
		defer attempt.Release()
	}

	ctx := c.Request.Context()
	factor, err := s.store.MFAFactorByID(ctx, user.ID, req.FactorID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && factor.ConfirmedAt != nil) {
		c.JSON(http.StatusNotFound, gin.H{"error": "that enrolment is not in progress; start again"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the factor"})
		return
	}
	now := time.Now().UTC()
	step, ok := auth.VerifyTOTP(factor.Secret, req.Code, 0, now)
	if !ok {
		if attempt != nil {
			s.signInFailed(c, attempt)
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "that code is not right — check the phone's clock and try the next one"})
		return
	}
	if attempt != nil {
		attempt.Succeed()
	}
	state, err := s.store.MFAState(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}

	var codes, hashes []string
	if !state.Enrolled() {
		codes, err = auth.NewRecoveryCodes()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create recovery codes"})
			return
		}
		hashes = make([]string, len(codes))
		for i, code := range codes {
			hashes[i] = auth.HashRecoveryCode(code)
		}
	}
	err = s.store.ConfirmMFAFactor(ctx, user.ID, factor.ID, step, now, hashes)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "that enrolment has expired; start again"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not confirm the factor"})
		return
	}
	s.recordMFAEvent(c, user, user, verbMFAEnroll, "")

	response := mfaConfirmResponse{RecoveryCodes: codes}
	if hash != "" {
		if !s.spendChallenge(c, hash) {
			return
		}
		session, ok := s.newSession(c, user, true)
		if !ok {
			return
		}
		response.Session = &mfaSessionResponse{loginResponse: session}
	}
	c.JSON(http.StatusOK, response)
}

// mfaSubject resolves whose factor a request is about: the session's account,
// or with a challenge given, the account an enrolment challenge was minted for.
// The hash is returned only in the second case.
func (s *server) mfaSubject(c *gin.Context, challenge string) (*db.User, string, bool) {
	if strings.TrimSpace(challenge) != "" {
		return s.challengeUser(c, challenge, db.MFAChallengeEnroll)
	}
	if _, ok := auth.ClaimsFrom(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
		return nil, "", false
	}
	user, ok := s.currentUser(c)
	return user, "", ok
}

// requireCurrentFactor proves a code before a change to the factors
// themselves, answering the request when it does not.
func (s *server) requireCurrentFactor(
	c *gin.Context, user *db.User, state db.MFAState, code, missing string,
) bool {
	if strings.TrimSpace(code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": missing})
		return false
	}
	attempt, ok := s.beginSignIn(c, mfaThrottleAccount(user.ID))
	if !ok {
		return false
	}
	defer attempt.Release()
	proof, err := s.proveFactor(c.Request.Context(), user, state, code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not check the second factor"})
		return false
	}
	if proof == "" {
		s.signInFailed(c, attempt)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "that code is not right"})
		return false
	}
	attempt.Succeed()
	if proof == proofRecovery {
		s.recordMFAEvent(c, user, user, verbMFARecoveryUsed, "")
	}
	return true
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

// removeMFAFactor removes one of the caller's factors. A code is required — a
// stolen session must not be able to strip the factor that would stop the
// thief at the next sign-in — and the last factor cannot go while the policy
// says the account must have one.
func (s *server) removeMFAFactor(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	if refuseMFAFor(c, caller) {
		return
	}
	id, ok := parseIDParam(c, "id", "factor")
	if !ok {
		return
	}
	var req mfaCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}
	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, caller.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	found := false
	for _, factor := range state.Factors {
		found = found || factor.ID == id
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "factor not found"})
		return
	}
	if len(state.Factors) == 1 && mfaRequiredFor(s.settings(ctx).MFAEnforcement, caller) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this is your only factor and your account must have one — add another before removing it",
		})
		return
	}
	if !s.requireCurrentFactor(c, caller, state, req.Code, "removing a factor needs a current code") {
		return
	}
	if err := s.store.DeleteMFAFactor(ctx, caller.ID, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "factor not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the factor"})
		return
	}
	s.recordMFAEvent(c, caller, caller, verbMFARemove, "")
	s.getMFA(c)
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// regenerateRecoveryCodes replaces the caller's recovery codes. The old set
// stops working in the same transaction.
func (s *server) regenerateRecoveryCodes(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	if refuseMFAFor(c, caller) {
		return
	}
	var req mfaCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}
	ctx := c.Request.Context()
	state, err := s.store.MFAState(ctx, caller.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	if !state.Enrolled() {
		c.JSON(http.StatusConflict, gin.H{"error": "recovery codes stand in for a factor; add one first"})
		return
	}
	if !s.requireCurrentFactor(c, caller, state, req.Code, "new recovery codes need a current code") {
		return
	}
	codes, err := auth.NewRecoveryCodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create recovery codes"})
		return
	}
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hashes[i] = auth.HashRecoveryCode(code)
	}
	if err := s.store.ReplaceMFARecoveryCodes(ctx, caller.ID, hashes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store recovery codes"})
		return
	}
	s.recordMFAEvent(c, caller, caller, verbMFARecoveryRegenerate, "")
	c.JSON(http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// getUserMFA is the administrator's view of somebody's second factor, for the
// access review. It never carries a secret.
func (s *server) getUserMFA(c *gin.Context) {
	_, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}
	status, err := s.mfaStatus(c.Request.Context(), target)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the second factor"})
		return
	}
	c.JSON(http.StatusOK, status)
}

type mfaResetResponse struct {
	FactorsRemoved int64 `json:"factors_removed"`
}

// resetUserMFA removes every factor and recovery code somebody holds, for the
// person who lost their phone. Never on your own account — the rule every
// self-protecting edit here follows, and the one that stops a stolen
// administrator session from quietly dropping its owner's factor — and only a
// super admin resets a super admin (loadManageableUser).
func (s *server) resetUserMFA(c *gin.Context) {
	caller, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}
	if target.ID == caller.ID {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "you cannot reset your own second factor — remove a factor from your profile instead",
		})
		return
	}
	if refuseMFAFor(c, target) {
		return
	}
	removed, err := s.store.ResetMFA(c.Request.Context(), target.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not reset the second factor"})
		return
	}
	s.recordMFAEvent(c, caller, target, verbMFAReset, "")
	c.JSON(http.StatusOK, mfaResetResponse{FactorsRemoved: removed})
}

// recordMFAEvent puts a factor's change in the trail with the identities
// crossed — who acted as the user, whose factor as the impersonated identity,
// the kubeconfig register's convention. Never the secret or a code.
func (s *server) recordMFAEvent(c *gin.Context, actor, subject *db.User, verb, failure string) {
	if s.auditor == nil {
		return
	}
	event := bastion.Event{
		At:       time.Now().UTC(),
		UserID:   actor.ID,
		Username: actor.Username,
		Verb:     verb,
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path,
		Resource: "users",
		Status:   http.StatusOK,
		Error:    failure,
	}
	if subject.ID != actor.ID {
		event.ImpersonatedUser = subject.Username
	}
	s.auditor.Record(c.Request.Context(), event)
}

// MFAResetStore is what the boot-time break-glass needs.
type MFAResetStore interface {
	UserByUsername(ctx context.Context, username string) (*db.User, error)
	ResetMFA(ctx context.Context, userID uint) (int64, error)
}

// BootUsername is who a change made by the server's own boot is attributed to.
const BootUsername = "kubemg:boot"

// ResetMFAAtBoot is the break-glass for KUBEMG_MFA_RESET_USERNAME: the one way
// back in for a sole super admin who lost both the phone and the recovery codes,
// short of editing the database. It is loud on purpose — an ERROR line every
// boot it runs, an audit row each time, and a posture warning for as long as
// the variable stays set, because left in place it strips that account's factor
// at every restart.
//
// An unknown account or one KubeMG's factor does not govern is logged and
// otherwise ignored: refusing to boot would turn a typo in a recovery step into
// an outage.
func ResetMFAAtBoot(ctx context.Context, store MFAResetStore, auditor bastion.Auditor, username string, logger *slog.Logger) {
	username = strings.TrimSpace(username)
	if username == "" {
		return
	}
	user, err := store.UserByUsername(ctx, username)
	if err != nil {
		logger.Error("KUBEMG_MFA_RESET_USERNAME names no account this server can reset; nothing was changed",
			slog.String("username", username), slog.String("error", err.Error()))
		return
	}
	if !user.MFAApplies() {
		logger.Error("KUBEMG_MFA_RESET_USERNAME names an account whose second factor is its identity provider's; nothing was changed",
			slog.String("username", username))
		return
	}
	removed, err := store.ResetMFA(ctx, user.ID)
	if err != nil {
		logger.Error("KUBEMG_MFA_RESET_USERNAME: could not reset the second factor",
			slog.String("username", username), slog.String("error", err.Error()))
		return
	}
	logger.Error("KUBEMG_MFA_RESET_USERNAME reset an account's second factor at boot — sign in, enrol again, and remove the variable now: it repeats at every restart",
		slog.String("username", username), slog.Int64("factors_removed", removed))
	if auditor != nil {
		auditor.Record(ctx, bastion.Event{
			At:               time.Now().UTC(),
			Username:         BootUsername,
			Verb:             verbMFAReset,
			Method:           "BOOT",
			Path:             "KUBEMG_MFA_RESET_USERNAME",
			Resource:         "users",
			Status:           http.StatusOK,
			ImpersonatedUser: user.Username,
		})
	}
}

// mfaChecks are the posture list's second-factor lines: the policy, any super
// admin who could still sign in with a password alone, and a break-glass left
// armed. They sit beside the deployment checks because a security review reads
// them in the same breath, but they are runtime state rather than boot facts.
func (s *server) mfaChecks(ctx context.Context) []setupCheck {
	var checks []setupCheck
	if s.deployment.MFAResetArmed {
		checks = append(checks, setupCheck{
			Key:      "mfa-reset-armed",
			Title:    "A second-factor reset runs at every restart",
			Severity: checkWarn,
			Detail: "KUBEMG_MFA_RESET_USERNAME is set, so the account it names loses its second factor " +
				"each time this server starts. It is a break-glass, not a setting.",
			Fix: "unset KUBEMG_MFA_RESET_USERNAME once the account has enrolled again",
		})
	}

	switch s.settings(ctx).MFAEnforcement {
	case mfaEnforcementAll:
		checks = append(checks, setupCheck{
			Key: "mfa", Title: "Every password sign-in needs a second factor", Severity: checkOK,
			Detail: "Local and LDAP accounts enrol at their next sign-in. OIDC and SAML accounts use their provider's.",
		})
	case mfaEnforcementAdmins:
		checks = append(checks, setupCheck{
			Key: "mfa", Title: "Administrators need a second factor", Severity: checkOK,
			Detail: "Other local and LDAP accounts may enrol and are not made to. OIDC and SAML accounts use their provider's.",
		})
	default:
		checks = append(checks, setupCheck{
			Key: "mfa", Title: "A second factor is optional for everyone", Severity: checkWarn,
			Detail: "A stolen administrator password is the whole console and, through it, every tunnel. " +
				"Requiring a factor for administrators closes that; sessions already open run to their own expiry.",
			Fix: "Settings → General → Second factor: Administrators",
		})
	}

	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return checks
	}
	enrolled, err := s.store.MFAEnrolledUserIDs(ctx)
	if err != nil {
		return checks
	}
	has := make(map[uint]bool, len(enrolled))
	for _, id := range enrolled {
		has[id] = true
	}
	bare := 0
	for i := range users {
		user := users[i]
		user.Normalize()
		if user.IsActive && user.IsSuperAdmin() && user.MFAApplies() && !has[user.ID] {
			bare++
		}
	}
	if bare > 0 {
		noun := "super admin signs"
		if bare > 1 {
			noun = "super admins sign"
		}
		checks = append(checks, setupCheck{
			Key:      "mfa-super-admins",
			Title:    fmt.Sprintf("%d %s in with a password alone", bare, noun),
			Severity: checkWarn,
			Detail: "A super admin can grant anything, including to itself. Its account page says who; " +
				"the account enrols from its own profile.",
		})
	}
	return checks
}
