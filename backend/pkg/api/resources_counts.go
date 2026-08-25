package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * How many of a thing a cluster has, without reading the thing.
 *
 * The Explore sidebar wants a number beside every entry, and the naive way to
 * get one is the way that cannot work here: a LIST per kind, in full, through a
 * tunnel that refuses a response over 8 MB. Thirty kinds on a thousand-pod
 * cluster is tens of megabytes to render a column of small grey numerals.
 *
 * So a count is read at `limit=1`. The API server only reports
 * `remainingItemCount` on a response it actually paginated, so asking for one
 * object is what makes it tell us about the rest — and the answer is one object
 * plus a number, whether the cluster holds ten of them or ten thousand. The cost
 * of a count is flat in the size of the cluster, which is the whole reason this
 * exists as its own route rather than as a field on each list.
 *
 * Three things keep it from being cheap in one place and expensive in another:
 *
 *   - It is a batch. One HTTP request carries every key the sidebar wants, so a
 *     console filling a column costs one round trip to KubeMG rather than
 *     thirty, and one entry in the read cache rather than thirty.
 *   - It is bounded. maxCountCalls caps the fan-out, because a namespace-scoped
 *     grant multiplies keys by namespaces and 30 x 25 is not a sidebar, it is a
 *     load test. Past the bound the caller is asked to pick a namespace, the
 *     same answer resourceScope already gives a list.
 *   - It is not on a timer. Every count is a real impersonated LIST against the
 *     cluster and lands in the audit trail as one, and `limit` makes the API
 *     server serve it from etcd rather than from its watch cache. That is an
 *     acceptable price for a question somebody asked by opening a section, and
 *     an unacceptable one every fifteen seconds for as long as a tab is open —
 *     which is why the console reads this lazily and never through useLiveTick.
 *
 * Nothing here is a new permission. A count goes down the same tunnel under the
 * same impersonated identity as the list it counts, so a kind the caller may not
 * list comes back unavailable with the cluster's own reason, exactly as the list
 * itself would answer.
 */

const (
	// maxCountKeys caps how many kinds one request may ask about. The fixed
	// inventory is around thirty; this leaves room for a cluster's own CRDs
	// without letting a caller name an arbitrary number of them.
	maxCountKeys = 48

	// maxCountCalls caps the reads one request will make once keys are
	// multiplied by the namespaces a scoped grant fans out over.
	maxCountCalls = 96

	// countConcurrency is how many count reads run at once. They are
	// independent reads over one already-multiplexed tunnel, so this is about
	// not putting a burst on the target API server rather than about this
	// process's own capacity.
	countConcurrency = 8
)

// resourceCountView is one kind's answer.
//
// Count is a pointer because "none" and "not known" are different: a cluster
// that paginated a list without reporting a remainder has told us there is more
// without saying how much, and printing the page size would be inventing a
// number.
type resourceCountView struct {
	Count     *int64 `json:"count,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Approximate marks a count that came from the API server's own
	// remainingItemCount rather than from a page that held the whole list. The
	// field exists because Kubernetes documents that number as an estimate; with
	// no selector on the read it is exact in practice, so the console prints it
	// plainly and this is here for anyone reading the API rather than the UI.
	Approximate bool `json:"approximate,omitempty"`
}

// countResourceKinds answers how many objects the caller can see of each named
// kind. Keys are the same strings the Explore sidebar uses for its lists.
func (s *server) countResourceKinds(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	keys, ok := countKeys(c)
	if !ok {
		return
	}

	// One namespace per key per group; the bound is checked before any read so
	// an over-wide request costs nothing rather than being cut off midway.
	perKey := len(scope.Namespaces)
	if perKey == 0 {
		perKey = 1
	}
	if len(keys)*perKey > maxCountCalls {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(
				"counting %d kinds across %d namespaces is %d reads, more than this view makes at once; pick one namespace or ask for fewer kinds",
				len(keys), perKey, len(keys)*perKey),
		})
		return
	}

	counts := make(map[string]resourceCountView, len(keys))
	var mu sync.Mutex

	// The gin context is never touched inside a worker: s.proxy.Call takes a
	// plain context and returns its result, so every answer — including a
	// failure — is folded into the map and written once, from here.
	ctx := c.Request.Context()
	var wg sync.WaitGroup
	slots := make(chan struct{}, countConcurrency)

	for _, key := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			view := s.countOne(ctx, user, cluster, grant, scope, key)
			mu.Lock()
			counts[key] = view
			mu.Unlock()
		}(key)
	}
	wg.Wait()

	c.JSON(http.StatusOK, gin.H{
		"counts":         counts,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// countKeys reads and validates the requested kinds.
func countKeys(c *gin.Context) ([]string, bool) {
	raw := c.QueryArray("keys")
	seen := map[string]bool{}
	keys := make([]string, 0, len(raw))
	for _, entry := range raw {
		for _, key := range strings.Split(entry, ",") {
			key = strings.TrimSpace(key)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name at least one resource to count"})
		return nil, false
	}
	if len(keys) > maxCountKeys {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("this view counts at most %d kinds at a time", maxCountKeys),
		})
		return nil, false
	}
	return keys, true
}

// countOne resolves a key to its API paths and sums what the cluster reports.
//
// An unknown key is reported rather than ignored: the sidebar and this table are
// two views of one inventory, and a key that reaches here without an entry means
// they have drifted, which is worth seeing in the response.
func (s *server) countOne(ctx context.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, scope readScope, key string,
) resourceCountView {
	kind, ok := objectKinds[key]
	custom := false
	if !ok {
		path, parsed := parseCustomKind(key)
		if !parsed {
			return resourceCountView{Reason: "this build does not know a resource called " + key}
		}
		// A CRD-served kind carries no scope in its key, so it is read under the
		// caller's own scope, which is right for a namespaced one. A
		// cluster-scoped custom resource answers 404 on every namespaced path;
		// that is what the fallback below is for.
		custom = true
		kind = objectKind{versions: []resourceListPath{path}, namespaced: true}
	}

	if !kind.namespaced && len(grant.NamespaceList()) > 0 {
		// The same refusal requireClusterScope gives a list, as an answer for
		// this one kind rather than for the whole batch: the other counts in
		// the request are still owed to the caller.
		return resourceCountView{
			Reason: key + " are not namespaced, and your access to this cluster is limited to " +
				strings.Join(grant.NamespaceList(), ", "),
		}
	}

	read := func(candidates []string) countResult {
		return s.countGroup(ctx, user, cluster, grant, candidates)
	}

	view := countOver(read, countPaths(kind, scope), key)

	// A cluster-scoped custom resource has just answered 404 on every namespaced
	// path. Where the caller holds no namespace scope, the cluster-wide path is
	// the one that answers it; where they do, a cluster-wide read was never
	// theirs to make and "not served" is the honest end of it.
	if custom && !view.Available && scope.Namespace != "" && len(grant.NamespaceList()) == 0 {
		wide := countPaths(objectKind{versions: kind.versions}, scope)
		if retry := countOver(read, wide, key); retry.Available {
			return retry
		}
	}
	return view
}

// countOver sums one kind across the namespace groups it is read over. It takes
// the read as a function for the same reason the paging loop does: the summing
// rules — a refusal on one namespace, a total the cluster would not report —
// are what a wrong number would come from, and they are worth pinning without a
// cluster.
func countOver(read func(candidates []string) countResult, groups [][]string, key string) resourceCountView {
	var total int64
	var approximate bool
	found := false
	unknown := false
	reason := ""

	for _, group := range groups {
		result := read(group)
		if !result.ok {
			return resourceCountView{Reason: result.reason}
		}
		if !result.found {
			// An empty reason is the 404 case — the cluster does not serve this
			// API — and a filled one is its own refusal, which is worth keeping
			// over a later namespace's silence.
			if result.reason != "" {
				reason = result.reason
			}
			continue
		}
		found = true
		if result.count == nil {
			unknown = true
			continue
		}
		total += *result.count
		approximate = approximate || result.approximate
	}

	switch {
	case !found:
		if reason == "" {
			reason = "this cluster does not serve " + key
		}
		return resourceCountView{Reason: reason}
	case unknown:
		// The cluster paginated without saying how much was left. Reporting the
		// page size would render four thousand pods as a confident "1".
		return resourceCountView{
			Available: true,
			Reason:    "this cluster did not report a total for " + key,
		}
	}
	return resourceCountView{Count: &total, Available: true, Approximate: approximate}
}

// countPaths renders the candidate path groups a count reads: one group per
// namespace a scope covers, each holding the API versions worth trying. A
// cluster-scoped kind is always read cluster-wide, even when a namespace is
// selected, because that selection is about the namespaced lists beside it.
func countPaths(kind objectKind, scope readScope) [][]string {
	if kind.namespaced {
		return scope.candidates(kind.versions...)
	}

	group := make([]string, 0, len(kind.versions))
	for _, version := range kind.versions {
		group = append(group, version.clusterWide())
	}
	return [][]string{group}
}

// countResult is one namespace group's answer. `ok=false` means the read failed
// outright; `found=false` with no reason means every candidate answered 404,
// which is the "not installed" answer rather than an error; a nil count on a
// found group means the cluster paginated without reporting a remainder.
type countResult struct {
	count       *int64
	approximate bool
	found       bool
	reason      string
	ok          bool
}

// countGroup asks one namespace's worth of candidates and returns the first that
// answers.
func (s *server) countGroup(ctx context.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, candidates []string,
) countResult {
	for _, path := range candidates {
		resp, err := s.proxy.Call(ctx, user, cluster, grant,
			http.MethodGet, pagedPath(path, countPageSize, ""), nil, nil)
		if err != nil {
			return countResult{reason: "could not read from the cluster"}
		}
		if resp.Status == http.StatusNotFound {
			continue
		}
		if resp.Status < 200 || resp.Status >= 300 {
			// A refusal is the cluster's own and is reported as this kind's
			// reason rather than failing the batch — a caller who may list pods
			// but not secrets is still owed the pod count.
			return countResult{reason: kubeErrorMessage(resp.Body, resp.Status), ok: true}
		}

		var page listPage[struct{}]
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return countResult{reason: "the cluster returned an unreadable response", ok: true}
		}
		return countFromPage(page)
	}
	return countResult{ok: true}
}

/*
 * countFromPage is the whole of the limit=1 arithmetic.
 *
 * An empty continue token means the page held the entire list — the cluster has
 * one object, or none — so its length is the count and nothing was estimated.
 *
 * Otherwise the total is this page plus what the API server says remains. And if
 * it says nothing, neither do we: the count is left absent rather than reported
 * as the page size, because a page size is one, and answering "1" for a cluster
 * running four thousand pods is not a smaller mistake than answering nothing.
 */
func countFromPage(page listPage[struct{}]) countResult {
	if page.Metadata.Continue == "" {
		total := int64(len(page.Items))
		return countResult{count: &total, found: true, ok: true}
	}
	if page.Metadata.RemainingItemCount == nil {
		return countResult{found: true, ok: true}
	}
	total := int64(len(page.Items)) + *page.Metadata.RemainingItemCount
	return countResult{count: &total, approximate: true, found: true, ok: true}
}
