package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// dummyPasswordHash is compared against on a login that can never succeed —
// an unknown username, or a federated account with no local password at all
// — so that branch spends roughly the same time as a real bcrypt check
// rather than returning in the time a map lookup takes. It is not a defense
// against enumeration by itself (see loginErrorAndStatus below, which is
// the actual fix), but a visibly-faster wrong-form branch would be a second,
// independent oracle next to the one being closed here.
const dummyPasswordHash = "$2a$10$65SBH3Wtnxyg/qRrfasRcu7FIZ9EMIHYWAMdJZh5NFfyiuRI4Isqm"

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type userResponse struct {
	ID         uint   `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email,omitempty"`
	Role       string `json:"role"`
	SystemRole string `json:"system_role"`
	IsActive   bool   `json:"is_active"`
	// CanViewRecordings is the recording-viewer capability, reported as the
	// server resolves it: a super admin holds it implicitly. The console reads it
	// to decide which affordances to draw, and the server still decides what a
	// request may see.
	CanViewRecordings bool `json:"can_view_recordings"`
	// CanRevealSecrets is the secret-reveal capability, likewise resolved: a
	// super admin holds it implicitly. The console reads it to decide whether to
	// draw the reveal control at all; the server still decides every request.
	CanRevealSecrets bool `json:"can_reveal_secrets"`
	// AuthSource says where this account's credentials live. The console reads
	// it to stop offering a password field for an account that has none.
	AuthSource string `json:"auth_source"`
	// AccountType separates a person from a programmatic caller. The console
	// reads it to stop offering a person's affordances to a machine.
	AccountType string     `json:"account_type"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type loginResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	User      userResponse `json:"user"`
}

func toUserResponse(u *db.User) userResponse {
	// Copy so the derived system role never depends on migration order for
	// accounts created before the IAM schema landed.
	normalized := *u
	normalized.Normalize()
	return userResponse{
		ID:                normalized.ID,
		Username:          normalized.Username,
		Email:             normalized.Email,
		Role:              normalized.Role,
		SystemRole:        normalized.SystemRole,
		IsActive:          normalized.IsActive,
		CanViewRecordings: normalized.MayViewAllRecordings(),
		CanRevealSecrets:  normalized.MayRevealSecrets(),
		AuthSource:        authSourceOf(normalized),
		AccountType:       accountTypeOf(normalized),
		LastLoginAt:       normalized.LastLoginAt,
		CreatedAt:         normalized.CreatedAt,
	}
}

// accountTypeOf reads a row written before machine accounts existed as the
// person it is.
func accountTypeOf(user db.User) string {
	if user.AccountType == db.AccountTypeMachine {
		return db.AccountTypeMachine
	}
	return db.AccountTypeUser
}

// authSourceOf renders an account's credential source, reading a row written
// before federation existed as the local account it is.
func authSourceOf(user db.User) string {
	if strings.TrimSpace(user.AuthSource) == "" {
		return db.AuthSourceLocal
	}
	return user.AuthSource
}

// login exchanges username/password credentials for a JWT.
func (s *server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	user, err := s.store.UserByUsername(c.Request.Context(), req.Username)
	if errors.Is(err, db.ErrNotFound) {
		// Spend the time a real check would, so this branch is not a visibly
		// faster oracle for "no such account" next to the ones below.
		auth.CheckPassword(dummyPasswordHash, req.Password)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not verify credentials"})
		return
	}

	// A federated account has no password here at all, so a submitted one is
	// never the right password rather than the wrong one — but saying so up
	// front, before any password is checked, let an unauthenticated caller
	// enumerate which usernames exist and are federated (any password gets a
	// distinct 403 with zero attempts). This answers the same as an unknown
	// username or a wrong local password: a federated account never signs in
	// here, so from the caller's side it is exactly that.
	//
	// A machine account is the same shape of answer for a different reason: it
	// holds no password at all, and authenticates only with a stored token
	// against one cluster's proxy.
	if user.IsMachine() || user.IsFederated() {
		auth.CheckPassword(dummyPasswordHash, req.Password)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// A disabled account keeps its grants but must not be able to sign in.
	if !user.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
		return
	}

	user.Normalize()
	token, expiresAt, err := s.jwt.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not issue token"})
		return
	}

	s.recordLogin(c, user.ID)

	c.JSON(http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      toUserResponse(user),
	})
}

// me returns the authenticated user's details.
func (s *server) me(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toUserResponse(user))
}

// currentUser resolves the JWT subject against the database, writing the error
// response itself when resolution fails.
func (s *server) currentUser(c *gin.Context) (*db.User, bool) {
	claims, ok := auth.ClaimsFrom(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
		return nil, false
	}

	user, err := s.store.UserByID(c.Request.Context(), claims.UserID)
	if errors.Is(err, db.ErrNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user no longer exists"})
		return nil, false
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not load user"})
		return nil, false
	}

	// Disabling an account has to take effect immediately, not when its
	// already-issued token happens to expire.
	if !user.IsActive {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
		return nil, false
	}

	user.Normalize()
	return user, true
}
