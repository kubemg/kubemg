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

// minPasswordLength is the shortest password a local account may be created
// with. Local accounts are the only credential in Phase 1, so this is the whole
// password policy.
const minPasswordLength = 8

type createUserRequest struct {
	Username   string `json:"username" binding:"required"`
	Email      string `json:"email" binding:"omitempty,email"`
	Password   string `json:"password" binding:"required"`
	SystemRole string `json:"system_role" binding:"required,oneof=superadmin admin user"`
	// CanViewRecordings grants the recording-viewer capability at creation.
	// Super-admin-only, like every other way of setting it.
	CanViewRecordings *bool `json:"can_view_recordings"`
}

type updateUserRequest struct {
	Username          *string `json:"username"`
	Email             *string `json:"email" binding:"omitempty,email"`
	Password          *string `json:"password"`
	SystemRole        *string `json:"system_role" binding:"omitempty,oneof=superadmin admin user"`
	CanViewRecordings *bool   `json:"can_view_recordings"`
}

// recordingCapabilityDenied refuses a caller who is not a super admin. Watching
// other people's shells is the most invasive read in the product, so the account
// that may hand that out is the same one that may create another super admin —
// otherwise an admin grants it to itself and the capability is decorative.
func recordingCapabilityDenied(c *gin.Context, caller *db.User) bool {
	if caller.IsSuperAdmin() {
		return false
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error": "only a super admin can grant or revoke access to other people's session recordings",
	})
	return true
}

type userStatusRequest struct {
	IsActive *bool `json:"is_active" binding:"required"`
}

// listUsers returns every local account (admin only).
func (s *server) listUsers(c *gin.Context) {
	users, err := s.store.ListUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list users"})
		return
	}

	out := make([]userResponse, 0, len(users))
	for i := range users {
		out = append(out, toUserResponse(&users[i]))
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

// createUser adds a local account (admin only).
func (s *server) createUser(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username is required"})
		return
	}
	if len(req.Password) < minPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "password must be at least 8 characters",
		})
		return
	}
	// Only a super admin may mint another one; otherwise an admin could grant
	// itself an account it is not allowed to touch afterwards.
	if req.SystemRole == db.SystemRoleSuperAdmin && !caller.IsSuperAdmin() {
		c.JSON(http.StatusForbidden, gin.H{"error": "only a super admin can create a super admin"})
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not hash the password"})
		return
	}

	// Asking for the capability is a separate authorization from asking for the
	// role, and is refused rather than ignored: an admin who thinks it was
	// granted would believe the wrong thing about who can see recordings.
	if req.CanViewRecordings != nil && *req.CanViewRecordings && recordingCapabilityDenied(c, caller) {
		return
	}

	user := db.User{
		Username:     username,
		Email:        strings.TrimSpace(req.Email),
		PasswordHash: hash,
		SystemRole:   req.SystemRole,
		IsActive:     true,
	}
	if req.CanViewRecordings != nil {
		user.CanViewRecordings = *req.CanViewRecordings
	}
	err = s.store.CreateUser(c.Request.Context(), &user)
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "username already taken"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the user"})
		return
	}

	c.JSON(http.StatusCreated, toUserResponse(&user))
}

// updateUser edits an account's details and system role (admin only).
func (s *server) updateUser(c *gin.Context) {
	caller, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	update := db.UserUpdate{Email: req.Email}
	if req.Username != nil {
		username := strings.TrimSpace(*req.Username)
		if username == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username cannot be empty"})
			return
		}
		update.Username = &username
	}
	if req.Password != nil {
		if len(*req.Password) < minPasswordLength {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not hash the password"})
			return
		}
		update.PasswordHash = &hash
	}
	if req.SystemRole != nil && *req.SystemRole != target.SystemRole {
		if target.ID == caller.ID {
			c.JSON(http.StatusForbidden, gin.H{"error": "you cannot change your own system role"})
			return
		}
		if *req.SystemRole == db.SystemRoleSuperAdmin && !caller.IsSuperAdmin() {
			c.JSON(http.StatusForbidden, gin.H{"error": "only a super admin can grant super admin"})
			return
		}
		update.SystemRole = req.SystemRole
	}
	if req.CanViewRecordings != nil && *req.CanViewRecordings != target.CanViewRecordings {
		if recordingCapabilityDenied(c, caller) {
			return
		}
		update.CanViewRecordings = req.CanViewRecordings
	}

	user, err := s.store.UpdateUser(c.Request.Context(), target.ID, update)
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "username already taken"})
		return
	}
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not update the user"})
		return
	}

	c.JSON(http.StatusOK, toUserResponse(user))
}

// setUserStatus enables or disables an account (admin only).
func (s *server) setUserStatus(c *gin.Context) {
	caller, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}

	var req userStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "is_active is required"})
		return
	}

	// Refusing self-disable is what keeps at least one active admin around:
	// the caller is always one, and it can only act on other accounts.
	if !*req.IsActive && target.ID == caller.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "you cannot disable your own account"})
		return
	}

	user, err := s.store.SetUserActive(c.Request.Context(), target.ID, *req.IsActive)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not update the user status"})
		return
	}

	c.JSON(http.StatusOK, toUserResponse(user))
}

// deleteUser removes an account (admin only).
func (s *server) deleteUser(c *gin.Context) {
	caller, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}

	if target.ID == caller.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "you cannot delete your own account"})
		return
	}

	err := s.store.DeleteUser(c.Request.Context(), target.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the user"})
		return
	}

	c.Status(http.StatusNoContent)
}

// loadManageableUser resolves the :id parameter and checks the caller is
// allowed to administer that account, writing the error response itself when it
// is not.
func (s *server) loadManageableUser(c *gin.Context) (caller *db.User, target *db.User, ok bool) {
	caller, ok = s.currentUser(c)
	if !ok {
		return nil, nil, false
	}

	id, ok := parseIDParam(c, "id", "user")
	if !ok {
		return nil, nil, false
	}

	target, err := s.store.UserByID(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return nil, nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the user"})
		return nil, nil, false
	}

	// A super admin account is only administrable by another super admin.
	if target.IsSuperAdmin() && !caller.IsSuperAdmin() {
		c.JSON(http.StatusForbidden, gin.H{"error": "only a super admin can manage a super admin"})
		return nil, nil, false
	}
	return caller, target, true
}

// recordLogin stamps a successful sign-in. A failure here must not block the
// user from signing in, so it is deliberately swallowed.
func (s *server) recordLogin(c *gin.Context, userID uint) {
	_ = s.store.TouchLastLogin(c.Request.Context(), userID, time.Now().UTC())
}
