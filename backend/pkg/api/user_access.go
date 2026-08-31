package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The one page that answers "what can this person reach today".
//
// It existed nowhere. The facts were all in the product — the permissions
// matrix, the group list, the JIT queue, the kubeconfig register, the session
// index — and an access review meant assembling them by hand from five screens,
// per person, on a schedule somebody signs their name against. What was missing
// was not data; it was the *join*.
//
// Two of those five stay where they are and are read by the page directly
// (`GET /api/v1/kubeconfigs?user_id=` and
// `GET /api/v1/audit/terminal-sessions?user_id=` both already narrow by user).
// What this route adds is the part that cannot be assembled in a browser without
// reimplementing the gateway: the **effective** grant per cluster, which is
// `db.MergeAccess` over direct, group-inherited and JIT rows, and which a second
// implementation in TypeScript would be free to disagree with. A page that says
// somebody holds `view` while the proxy grants `edit` is worse than no page.
//
// So the merge happens here, with the same function the proxy uses, and the
// contributing grants are reported alongside it — because "why" is the half an
// access review actually needs. An effective `cluster-admin` on production reads
// very differently depending on whether it came from a standing grant somebody
// wrote in 2024, a directory group, or an approved elevation that ends in forty
// minutes.

// accessGrantOrigin says where one contributing grant came from.
const (
	// grantOriginDirect is a row written against this user: by an administrator,
	// by the federation sync, or by an approved JIT request. Which of the three
	// is the grant's own Source.
	grantOriginDirect = "direct"
	// grantOriginGroup is inherited through a group membership.
	grantOriginGroup = "group"
)

// contributingGrant is one reason a person can reach a cluster. It is
// deliberately not the effective answer — several of these merge into that.
type contributingGrant struct {
	Origin string `json:"origin"`
	// Source is the grant's own provenance for a direct row: `local` (an
	// administrator wrote it), `sso` (the directory asserts it) or `jit` (an
	// approved elevation). Empty for a group-inherited row, whose provenance is
	// the group itself.
	Source string `json:"source,omitempty"`
	// Group names the membership a row was inherited through.
	Group   string `json:"group,omitempty"`
	GroupID uint   `json:"group_id,omitempty"`
	K8sRole string `json:"k8s_role"`
	// Namespaces empty means cluster-wide, exactly as it does on the grant.
	Namespaces []string   `json:"namespaces"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// clusterAccess is what one person can reach on one cluster, and why.
type clusterAccess struct {
	ClusterID   uint   `json:"cluster_id"`
	Cluster     string `json:"cluster"`
	Environment string `json:"environment"`
	ShortName   string `json:"short_name,omitempty"`
	// K8sRole, Namespaces and ExpiresAt are the *effective* answer — what the
	// proxy will actually allow — resolved by db.MergeAccess over Grants below.
	K8sRole    string              `json:"k8s_role"`
	Namespaces []string            `json:"namespaces"`
	ExpiresAt  *time.Time          `json:"expires_at,omitempty"`
	Grants     []contributingGrant `json:"grants"`
}

// groupMembership is one group this person is in, and who put them there.
type groupMembership struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	// Source is `local` for a membership an administrator wrote and `sso` for
	// one the federation sync derived. Only the derived ones are reconciled away
	// when the directory stops asserting the group, which is the difference an
	// access review cares about: one of them is somebody's standing decision and
	// the other is a mirror of a system nobody here controls.
	Source string `json:"source,omitempty"`
}

type userAccessResponse struct {
	User userResponse `json:"user"`
	// Provider names the identity provider a federated account signs in through.
	// The account's `auth_source` says *that* it is federated; without the name,
	// an auditor asking "which directory owns this identity" has to go and read
	// the SSO settings page and match an id by hand.
	Provider string            `json:"provider,omitempty"`
	Groups   []groupMembership `json:"groups"`
	Clusters []clusterAccess   `json:"clusters"`
}

// userAccess reports everything KubeMG's own records say about what one person
// can reach (admin only).
//
// It is admin-only rather than "narrowed to your own", which is the opposite of
// the rule the audit trail and the kubeconfig register follow. The difference is
// what the page is for: those two are records of things *you* did and are
// therefore yours to read, whereas this is the review surface — it exists to be
// read *about* somebody, and a person reading their own would learn nothing here
// that `/me/access` does not already tell them.
func (s *server) userAccess(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	ctx := c.Request.Context()
	user, err := s.store.UserByID(ctx, uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the user"})
		return
	}

	out := userAccessResponse{
		User:     toUserResponse(user),
		Groups:   []groupMembership{},
		Clusters: []clusterAccess{},
	}

	// The provider's name, where there is one. A provider that has since been
	// deleted leaves the account federated with nothing to name, which reads as
	// absent rather than as an error: the account is still exactly as federated
	// as it was, and refusing the whole page over a missing label would be the
	// wrong trade on a review surface.
	if user.SSOProviderID != 0 {
		if provider, err := s.store.SSOProviderByID(ctx, user.SSOProviderID); err == nil {
			out.Provider = provider.Name
		}
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load groups"})
		return
	}
	groupNames := map[uint]string{}
	for _, group := range groups {
		groupNames[group.ID] = group.Name
	}

	memberships, err := s.store.GroupMembershipsForUser(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load group memberships"})
		return
	}
	memberOf := map[uint]bool{}
	for _, membership := range memberships {
		name, known := groupNames[membership.GroupID]
		if !known {
			// A membership of a group that has been deleted grants nothing, and
			// naming a row nobody can act on would be noise on a page whose job
			// is to be read line by line.
			continue
		}
		memberOf[membership.GroupID] = true
		out.Groups = append(out.Groups, groupMembership{
			ID:     membership.GroupID,
			Name:   name,
			Source: membership.Source,
		})
	}
	sort.SliceStable(out.Groups, func(i, j int) bool { return out.Groups[i].Name < out.Groups[j].Name })

	userGrants, err := s.store.ListUserAccess(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load grants"})
		return
	}
	groupGrants, err := s.store.ListGroupAccess(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load group grants"})
		return
	}
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load clusters"})
		return
	}

	now := time.Now().UTC()
	effective := map[uint]db.UserClusterAccess{}
	contributing := map[uint][]contributingGrant{}

	for _, grant := range userGrants {
		if grant.UserID != user.ID || !liveGrant(grant, now) {
			continue
		}
		merge(effective, grant.ClusterID, grant)
		contributing[grant.ClusterID] = append(contributing[grant.ClusterID], contributingGrant{
			Origin:     grantOriginDirect,
			Source:     grant.Source,
			K8sRole:    grant.K8sRole,
			Namespaces: namespaceList(grant),
			ExpiresAt:  grant.ExpiresAt,
		})
	}

	for _, grant := range groupGrants {
		if !memberOf[grant.GroupID] {
			continue
		}
		// A group grant has no expiry of its own — only a direct row carries one
		// — so it enters the merge as a standing grant, which is what it is.
		candidate := db.UserClusterAccess{
			UserID:     user.ID,
			ClusterID:  grant.ClusterID,
			K8sRole:    grant.K8sRole,
			Namespaces: grant.Namespaces,
		}
		merge(effective, grant.ClusterID, candidate)
		contributing[grant.ClusterID] = append(contributing[grant.ClusterID], contributingGrant{
			Origin:     grantOriginGroup,
			Group:      groupNames[grant.GroupID],
			GroupID:    grant.GroupID,
			K8sRole:    grant.K8sRole,
			Namespaces: candidate.NamespaceList(),
		})
	}

	byID := map[uint]db.Cluster{}
	for _, cluster := range clusters {
		byID[cluster.ID] = cluster
	}

	for clusterID, resolved := range effective {
		cluster, known := byID[clusterID]
		if !known {
			// A grant against a cluster that no longer exists is not access to
			// anything, and naming a row nobody can act on would be noise on a
			// page whose whole job is to be reviewed line by line.
			continue
		}
		grants := contributing[clusterID]
		sort.SliceStable(grants, func(i, j int) bool {
			return grants[i].Origin < grants[j].Origin
		})
		out.Clusters = append(out.Clusters, clusterAccess{
			ClusterID:   cluster.ID,
			Cluster:     cluster.Name,
			Environment: cluster.Environment,
			ShortName:   cluster.ShortName,
			K8sRole:     resolved.K8sRole,
			Namespaces:  namespaceList(resolved),
			ExpiresAt:   resolved.ExpiresAt,
			Grants:      grants,
		})
	}

	// Production first, then by name: a review is read top-down, and the rows
	// that decide whether it is signed are the ones on the clusters that matter.
	sort.SliceStable(out.Clusters, func(i, j int) bool {
		a, b := environmentRank(out.Clusters[i].Environment), environmentRank(out.Clusters[j].Environment)
		if a != b {
			return a < b
		}
		return out.Clusters[i].Cluster < out.Clusters[j].Cluster
	})

	c.JSON(http.StatusOK, out)
}

// merge folds one grant into the effective answer with the same function the
// gateway uses. Calling db.MergeAccess rather than restating its rules is the
// point: this page must not be able to disagree with what the proxy allows.
func merge(into map[uint]db.UserClusterAccess, clusterID uint, grant db.UserClusterAccess) {
	if existing, ok := into[clusterID]; ok {
		into[clusterID] = db.MergeAccess(existing, grant)
		return
	}
	into[clusterID] = grant
}

// liveGrant is db.AccessForUser's rule restated for an in-memory list: a grant
// with no expiry, or one whose expiry has not passed. Expiry is enforced on
// every read rather than by the sweeper, so a window that has run out is closed
// whether or not any background pass has run since — and a review that showed an
// expired elevation as live would be the exact failure that rule exists to stop.
func liveGrant(grant db.UserClusterAccess, now time.Time) bool {
	return grant.ExpiresAt == nil || grant.ExpiresAt.After(now)
}

// namespaceList renders a grant's scope, with cluster-wide as an empty list
// rather than a null — the console draws "all namespaces" from the emptiness,
// and a null would make that a different code path for no reason.
func namespaceList(grant db.UserClusterAccess) []string {
	out := grant.NamespaceList()
	if out == nil {
		return []string{}
	}
	return out
}

// environmentRank orders a review by what it costs to get wrong.
func environmentRank(environment string) int {
	switch environment {
	case db.EnvProd:
		return 0
	case db.EnvStaging:
		return 1
	default:
		return 2
	}
}
