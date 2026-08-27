package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// verbPasswordChange is what rotating your own credential is called in the
// trail. It is a KubeMG act rather than a cluster one, so it carries no
// cluster — the same shape as a kubeconfig issuance that names no namespace.
const verbPasswordChange = "password-change"

// changePasswordRequest is a rotation asked for by the account that owns the
// password.
//
// The current password is required and is not a formality: a stolen session is
// exactly the case this route must not serve, and re-authenticating is the one
// thing a session cannot do on its holder's behalf. Without it, anybody holding
// a live token could lock the owner out of their own account.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
	// RevokeKubeconfigs takes this account's issued credentials with the
	// rotation. It is offered rather than done: somebody rotating a password
	// because they suspect it leaked wants the kubeconfigs gone too, and
	// somebody rotating one on a schedule does not want every laptop in the team
	// to stop working. Doing it silently would be the wrong answer to both.
	RevokeKubeconfigs bool `json:"revoke_kubeconfigs"`
}

// changePasswordResponse reports the rotation and, when one was asked for, what
// the blanket revoke actually reached — the same summary the credential
// register's own route returns, because the honesty about direct mode is the
// same honesty here.
type changePasswordResponse struct {
	Changed     bool                          `json:"changed"`
	Credentials *revokeAllKubeconfigsResponse `json:"credentials,omitempty"`
}

// changePassword rotates the authenticated account's own password.
//
// Three accounts are refused rather than failing oddly. A federated account has
// no local password to rotate and is told so, because being handed a form that
// cannot work is worse than being told where the password actually lives. A
// machine account holds no password by construction — its credential is a stored
// token with its own revoke. A disabled account never reaches here at all:
// currentUser refuses it before this handler runs.
//
// The strength rule is minPasswordLength, the one the account was created
// under. One policy, not two.
func (s *server) changePassword(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "the current and new passwords are both required",
		})
		return
	}

	if caller.IsMachine() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "a machine account has no password — rotate its token instead",
		})
		return
	}
	if caller.IsFederated() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this account signs in through an identity provider, so its password " +
				"is not held here — change it with your provider",
		})
		return
	}

	if len(req.NewPassword) < minPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
		return
	}

	if !auth.CheckPassword(caller.PasswordHash, req.CurrentPassword) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "the current password is incorrect"})
		return
	}
	// A rotation that rotates nothing is refused rather than reported as done:
	// the caller believes the old password is now dead, and it is not.
	if req.NewPassword == req.CurrentPassword {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "the new password must be different from the current one",
		})
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not hash the password"})
		return
	}

	ctx := c.Request.Context()
	if _, err := s.store.UpdateUser(ctx, caller.ID, db.UserUpdate{PasswordHash: &hash}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not change the password"})
		return
	}

	s.recordPasswordChange(c, caller)

	out := changePasswordResponse{Changed: true}
	if req.RevokeKubeconfigs {
		summary, ok := s.revokeOwnKubeconfigs(c, caller)
		if !ok {
			return
		}
		out.Credentials = &summary
	}
	c.JSON(http.StatusOK, out)
}

// revokeOwnKubeconfigs is the credential register's blanket revoke, called for
// the account that just rotated its password. It reuses the register's own
// summary and audit records so that a revoke performed here is indistinguishable
// in the trail from one performed on the credentials page — it is the same act.
func (s *server) revokeOwnKubeconfigs(c *gin.Context, caller *db.User) (revokeAllKubeconfigsResponse, bool) {
	ctx := c.Request.Context()
	revoked, err := s.store.RevokeKubeconfigsForUser(ctx, caller.ID, time.Now().UTC(), caller.ID, caller.Username)
	if err != nil {
		// The password is already changed at this point, so the revoke failing is
		// not a reason to report the rotation as failed — but it is emphatically
		// not something to report as done either.
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "the password was changed, but the issued credentials could not be revoked",
		})
		return revokeAllKubeconfigsResponse{}, false
	}
	s.publishRevokedCredentials(ctx)
	s.recordKubeconfigRevocation(c, caller, revoked)
	return summariseBlanketRevoke(revoked), true
}

// recordPasswordChange puts the rotation in the trail. Only the act is
// recorded — never the password, in any form.
func (s *server) recordPasswordChange(c *gin.Context, caller *db.User) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:       time.Now().UTC(),
		UserID:   caller.ID,
		Username: caller.Username,
		Verb:     verbPasswordChange,
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path,
		Resource: "users",
		Status:   http.StatusOK,
	})
}
