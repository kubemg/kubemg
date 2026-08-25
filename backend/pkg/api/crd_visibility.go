package api

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"

	"github.com/gin-gonic/gin"
)

/*
 * Curating a cluster's custom resources.
 *
 * Explore's custom-resource sections are derived from the cluster's own CRD
 * list, which is what lets it browse an operator nobody here has heard of. The
 * cost of deriving them is that a cluster running three operators declares a
 * hundred kinds, and most are one operator talking to itself — a lock, an
 * internal revision, a generated certificate request. They are reachable, and
 * nobody browses them.
 *
 * So an administrator says which of them the sidebar offers, per cluster, and
 * everybody else gets that list. Three rules hold:
 *
 *   - **Reading the curation is as wide as the cluster.** Anyone the cluster is
 *     granted to gets the same answer the sidebar was built from; only an admin
 *     may write it. That is the consoles rule, for the same reason — a reader
 *     has to be able to tell "this cluster does not run Istio" from "somebody
 *     took Istio off the list".
 *   - **Hiding is not refusing.** A hidden kind leaves the navigation and
 *     nothing else: the custom-resource read, the manifest editor and every
 *     object route still address it exactly as kubectl would, and what may
 *     actually be read stays the cluster's own RBAC to decide. Treating this as
 *     a permission would be claiming an access control that a `kubectl get`
 *     disproves in one command.
 *   - **The default is shown.** The stored set is what is *hidden*, so an
 *     install that has never opened this panel behaves exactly as it always did,
 *     and installing a new operator puts its kinds in the sidebar rather than
 *     hiding them until somebody notices.
 */

// maxHiddenCRDs bounds one cluster's curation. A cluster serving more distinct
// kinds than this has bigger problems than its sidebar, and an unbounded list
// arriving on a PUT is a write nobody meant to make.
const maxHiddenCRDs = 500

// crdResourcePattern is `plural.group`, the way kubectl names a resource
// unambiguously. The group is mandatory — a custom resource is never in the core
// group — which is also what keeps `pods` out of a set that only ever describes
// CRDs.
var crdResourcePattern = regexp.MustCompile(
	`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)

// crdVisibilityRequest is the whole curation, submitted at once: an
// administrator looks at what a cluster serves and says which of it is worth
// showing, so what is stored is a replacement rather than a stream of edits.
type crdVisibilityRequest struct {
	Hidden []string `json:"hidden"`
}

// showClusterCRDVisibility returns which custom resources this cluster's sidebar
// leaves out, and whether the caller may change that.
func (s *server) showClusterCRDVisibility(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	hidden, err := s.store.HiddenCRDs(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "could not load this cluster’s resource visibility"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"hidden":   hidden,
		"editable": user.IsAdmin(),
	})
}

// putClusterCRDVisibility replaces the hidden set (admin only).
func (s *server) putClusterCRDVisibility(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	var req crdVisibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hidden, err := normalizeHiddenCRDs(req.Hidden)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.store.SetHiddenCRDs(c.Request.Context(), cluster.ID, hidden, user.ID); err != nil {
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "could not save this cluster’s resource visibility"})
		return
	}

	// The CRD list this decides is served through the read cache, keyed per
	// caller — so without this every session holds its old sidebar until the
	// entry ages out. A write changing what a cluster's reads answer drops the
	// cluster's scope, exactly as a scale or a manifest PUT does.
	if s.reads != nil {
		s.reads.InvalidateScope("cluster:" + c.Param("id"))
	}

	c.JSON(http.StatusOK, gin.H{"hidden": hidden, "editable": true})
}

// normalizeHiddenCRDs validates, deduplicates and orders a submitted set.
func normalizeHiddenCRDs(submitted []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(submitted))

	for _, resource := range submitted {
		if resource == "" || seen[resource] {
			continue
		}
		if !crdResourcePattern.MatchString(resource) {
			return nil, fmt.Errorf("a hidden resource is named plural.group, like "+
				"virtualservices.networking.istio.io — got %q", resource)
		}
		if len(out) >= maxHiddenCRDs {
			return nil, fmt.Errorf("a cluster can hide at most %d custom resources", maxHiddenCRDs)
		}
		seen[resource] = true
		out = append(out, resource)
	}

	sort.Strings(out)
	return out, nil
}

// hiddenCRDSet reads a cluster's curation for a list that is about to be
// filtered by it.
//
// A store failure is answered as "nothing is hidden" rather than as an error:
// this is navigation, the underlying reads are governed by the cluster's own
// RBAC either way, and a database blip that empties a developer's sidebar is a
// worse failure than one that briefly shows a kind an administrator tidied away.
func (s *server) hiddenCRDSet(c *gin.Context, clusterID uint) map[string]bool {
	hidden, err := s.store.HiddenCRDs(c.Request.Context(), clusterID)
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(hidden))
	for _, resource := range hidden {
		set[resource] = true
	}
	return set
}

// applyCRDVisibility is what the CRD list does with that set. An administrator
// sees every kind with the hidden ones marked, because they are the one who has
// to be able to put one back; everybody else sees the curated list, because a
// row they cannot act on and were not meant to browse is exactly what was being
// removed.
func applyCRDVisibility(views []crdView, hidden map[string]bool, admin bool) []crdView {
	if len(hidden) == 0 {
		return views
	}

	out := make([]crdView, 0, len(views))
	for _, view := range views {
		if !hidden[view.Plural+"."+view.Group] {
			out = append(out, view)
			continue
		}
		if !admin {
			continue
		}
		view.Hidden = true
		out = append(out, view)
	}
	return out
}
