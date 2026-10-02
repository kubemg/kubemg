package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// verbProfileUpdate is what editing your own account details is called in the
// trail. Like password-change it is a KubeMG act and names no cluster.
const verbProfileUpdate = "profile-update"

// updateProfileRequest is the part of an account its owner may edit.
//
// Only the email. The username is deliberately absent: it is the identity the
// gateway impersonates (`kubemg:u:<username>`), the name every audit record
// carries and the key a federated account is matched on, so renaming it is an
// administrator's act on PUT /users/:id, never a self-service one. The role and
// both capabilities are absent for the obvious reason.
type updateProfileRequest struct {
	Email *string `json:"email"`
}

// emailRule is the address check an administrator's PUT /users/:id applies,
// run on the trimmed value — one policy, not two. A pointer field cannot carry
// it directly: omitempty skips only nil, so clearing the address would fail.
type emailRule struct {
	Email string `binding:"omitempty,email"`
}

// updateProfile edits the authenticated account's own details.
//
// Refused rather than failing oddly, on the same terms as changePassword: a
// federated account's email is the directory's — every sign-in that carries one
// writes it back — so an edit here would be silently undone; a machine account
// is administered, not self-served. An edit that changes nothing is answered,
// not written, so the trail never carries an update that updated nothing.
func (s *server) updateProfile(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}

	if caller.IsMachine() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "a machine account is administered, not self-served — ask an administrator",
		})
		return
	}
	if caller.IsFederated() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this account's details come from its identity provider and are " +
				"rewritten at every sign-in — change them with your provider",
		})
		return
	}

	if req.Email == nil {
		c.JSON(http.StatusOK, toUserResponse(caller))
		return
	}
	email := strings.TrimSpace(*req.Email)
	if err := binding.Validator.ValidateStruct(emailRule{Email: email}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the email address is not valid"})
		return
	}
	if email == caller.Email {
		c.JSON(http.StatusOK, toUserResponse(caller))
		return
	}

	updated, err := s.store.UpdateUser(c.Request.Context(), caller.ID, db.UserUpdate{Email: &email})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not update the profile"})
		return
	}

	s.recordProfileUpdate(c, caller)
	c.JSON(http.StatusOK, toUserResponse(updated))
}

// recordProfileUpdate puts the edit in the trail. The act is recorded, not the
// address — the account row is where the current value lives.
func (s *server) recordProfileUpdate(c *gin.Context, caller *db.User) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:       time.Now().UTC(),
		UserID:   caller.ID,
		Username: caller.Username,
		Verb:     verbProfileUpdate,
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path,
		Resource: "users",
		Status:   http.StatusOK,
	})
}
