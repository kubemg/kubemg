package api

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// machineAccountName is what a programmatic account may be called. It is
// stricter than a person's username on purpose: this string is sent to the
// target cluster as Impersonate-User, so it has to be something a Kubernetes
// RoleBinding can name, and an operator reading `kubectl auth can-i --as` output
// has to recognise it.
var machineAccountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$`)

type createMachineAccountRequest struct {
	Username string `json:"username" binding:"required"`
	// Email is the owner: who to ask about a credential nobody recognises. It is
	// the one thing that makes an abandoned token actionable rather than merely
	// visible.
	Email string `json:"email" binding:"omitempty,email"`
}

// machineAccountResponse is an account plus what an administrator needs to
// decide anything about it — how many live credentials it holds, and when one of
// them was last used. A list of machine identities with no usage on it is a list
// nobody can prune.
type machineAccountResponse struct {
	userResponse
	TokenCount     int                    `json:"token_count"`
	ActiveTokens   int                    `json:"active_tokens"`
	LastUsedAt     *time.Time             `json:"last_used_at,omitempty"`
	ClusterSummary []machineAccountAccess `json:"access"`
}

// machineAccountAccess is one line of the account's standing grant, resolved the
// same way every other read resolves it — through AccessForUser, so a grant
// inherited from a group counts and an expired elevation does not.
type machineAccountAccess struct {
	ClusterID   uint     `json:"cluster_id"`
	ClusterName string   `json:"cluster_name"`
	K8sRole     string   `json:"k8s_role"`
	Namespaces  []string `json:"namespaces"`
}

// listMachineAccounts returns every programmatic account (admin only).
func (s *server) listMachineAccounts(c *gin.Context) {
	ctx := c.Request.Context()

	users, err := s.store.ListUsers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list machine accounts"})
		return
	}
	tokens, err := s.store.ListMachineTokens(ctx, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list machine account tokens"})
		return
	}
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load clusters"})
		return
	}
	clusterNames := make(map[uint]string, len(clusters))
	for _, cluster := range clusters {
		clusterNames[cluster.ID] = cluster.Name
	}

	now := time.Now().UTC()
	out := []machineAccountResponse{}
	for i := range users {
		user := &users[i]
		if !user.IsMachine() {
			continue
		}
		row := machineAccountResponse{userResponse: toUserResponse(user)}
		for j := range tokens {
			token := tokens[j]
			if token.UserID != user.ID {
				continue
			}
			row.TokenCount++
			if token.Usable(now) {
				row.ActiveTokens++
			}
			if token.LastUsedAt != nil && (row.LastUsedAt == nil || token.LastUsedAt.After(*row.LastUsedAt)) {
				row.LastUsedAt = token.LastUsedAt
			}
		}
		// A grant read per account is a handful of rows on any real install, and
		// an account list that does not say what each one can reach is a list of
		// names.
		grants, err := s.store.AccessForUser(ctx, user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
			return
		}
		row.ClusterSummary = []machineAccountAccess{}
		for clusterID, grant := range grants {
			row.ClusterSummary = append(row.ClusterSummary, machineAccountAccess{
				ClusterID:   clusterID,
				ClusterName: clusterNames[clusterID],
				K8sRole:     grant.K8sRole,
				Namespaces:  grant.NamespaceList(),
			})
		}
		sortMachineAccountAccess(row.ClusterSummary)
		out = append(out, row)
	}

	c.JSON(http.StatusOK, gin.H{"service_accounts": out})
}

// createMachineAccount adds a programmatic identity (admin only). It holds no
// password at all — not an unusable one — because the account never signs in
// anywhere: its only credential is a token issued against one cluster.
func (s *server) createMachineAccount(c *gin.Context) {
	var req createMachineAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	username := strings.ToLower(strings.TrimSpace(req.Username))
	if !machineAccountName.MatchString(username) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a machine account name must be 3–64 lowercase letters, digits, dots, dashes or " +
				"underscores, starting and ending with a letter or digit — it is sent to the cluster " +
				"as the impersonated user",
		})
		return
	}

	user := db.User{
		Username:    username,
		Email:       strings.TrimSpace(req.Email),
		AccountType: db.AccountTypeMachine,
		SystemRole:  db.SystemRoleUser,
		IsActive:    true,
	}
	user.Normalize()

	err := s.store.CreateUser(c.Request.Context(), &user)
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "that name is already taken by an account"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the machine account"})
		return
	}

	c.JSON(http.StatusCreated, machineAccountResponse{
		userResponse:   toUserResponse(&user),
		ClusterSummary: []machineAccountAccess{},
	})
}

// setMachineAccountStatus enables or disables an account. Disabling is the
// blunt lever — it stops every token the account holds at once — where revoking
// one token stops one pipeline.
func (s *server) setMachineAccountStatus(c *gin.Context) {
	target, ok := s.loadMachineAccount(c)
	if !ok {
		return
	}

	var req userStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "is_active is required"})
		return
	}

	user, err := s.store.SetUserActive(c.Request.Context(), target.ID, *req.IsActive)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "machine account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not update the machine account"})
		return
	}

	c.JSON(http.StatusOK, machineAccountResponse{userResponse: toUserResponse(user)})
}

// deleteMachineAccount removes the account, its grants and its tokens.
func (s *server) deleteMachineAccount(c *gin.Context) {
	target, ok := s.loadMachineAccount(c)
	if !ok {
		return
	}

	err := s.store.DeleteUser(c.Request.Context(), target.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "machine account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the machine account"})
		return
	}

	c.Status(http.StatusNoContent)
}

// loadMachineAccount resolves the :id parameter, refusing a person's account.
// The two surfaces are kept apart in both directions: /users refuses a service
// account and this refuses a human one, because the affordances differ — a
// person has a password and may be an administrator, a machine has neither.
func (s *server) loadMachineAccount(c *gin.Context) (*db.User, bool) {
	id, ok := parseIDParam(c, "id", "machine account")
	if !ok {
		return nil, false
	}

	user, err := s.store.UserByID(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "machine account not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the machine account"})
		return nil, false
	}
	if !user.IsMachine() {
		c.JSON(http.StatusNotFound, gin.H{"error": "machine account not found"})
		return nil, false
	}
	user.Normalize()
	return user, true
}

func sortMachineAccountAccess(rows []machineAccountAccess) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].ClusterName < rows[j].ClusterName })
}
