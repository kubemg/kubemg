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
	// AuthSource says where this account's credentials live. The console reads
	// it to stop offering a password field for an account that has none.
	AuthSource  string     `json:"auth_source"`
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
		ID:          normalized.ID,
		Username:    normalized.Username,
		Email:       normalized.Email,
		Role:        normalized.Role,
		SystemRole:  normalized.SystemRole,
		IsActive:    normalized.IsActive,
		CanViewRecordings: normalized.MayViewAllRecordings(),
		AuthSource:  authSourceOf(normalized),
		LastLoginAt: normalized.LastLoginAt,
		CreatedAt:   normalized.CreatedAt,
	}
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not verify credentials"})
		return
	}

	// A federated account has no password here at all, so this is not a wrong
	// one — it is the wrong form. Saying which provider owns the account is what
	// stops someone trying their directory password against this box until they
	// are locked out of the directory instead.
	if user.IsFederated() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "this account signs in through an identity provider",
		})
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
