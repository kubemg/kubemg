package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Reading a list without reading all of it at once.
 *
 * Kubernetes has no count and no bounded list: a LIST returns every object, in
 * full, and the only lever a client has is `limit` plus the `continue` token the
 * API server hands back. That lever is not optional here, for a reason that is
 * specific to this product rather than general good practice.
 *
 * A read travels through the agent, and the agent caps what it will carry back
 * from the API server at 8 MB (`maxResponseBody` in agent/internal/kube). A pod
 * object is roughly 8-15 KB once managedFields and containerStatuses are in it,
 * so a thousand-pod cluster answers an all-namespaces pod list with something in
 * the region of 10 MB — which the agent refuses outright, with a message about
 * the response being too large to tunnel. Unpaginated, a list view is not slow
 * on a real cluster; it is broken, and broken non-deterministically, because the
 * threshold moves as the pods do.
 *
 * So every list read here pages, and the two bounds are chosen against different
 * failures:
 *
 *   - listPageSize bounds one *frame*. It exists to stay under the agent's
 *     ceiling with room to spare, and the cost of setting it low is round trips
 *     rather than a refusal.
 *   - maxListItems bounds the whole *read*. Paging alone would happily pull a
 *     hundred megabytes through the tunnel one safe frame at a time, which is
 *     the same problem arriving more politely. Past this bound the read stops
 *     and says so — `truncated` travels in the response and the console states
 *     it, because a list showing 2000 of 12000 rows with nothing to mark the
 *     difference is worse than one that refuses.
 *
 * A `limit` on a LIST also means the API server serves it from etcd rather than
 * from its watch cache. That is a real cost on the control plane and it is the
 * reason counts below are read at limit=1 and never put on a timer.
 */

const (
	// listPageSize is how many objects one page of a list read asks for. Pods
	// are the largest object commonly listed here, so 250 of them is roughly
	// 2.5 MB — comfortably inside the agent's 8 MB ceiling even for a cluster
	// whose pods are unusually fat.
	listPageSize = 250

	// maxListItems is the most one list read will collect across all its pages
	// and, for an all-namespaces read on a scoped grant, across all of its
	// namespaces. A console list nobody scrolls to the end of does not justify
	// pulling an unbounded number of objects through the tunnel.
	maxListItems = 2000

	// countPageSize is what a count read asks for. It cannot be zero: the API
	// server only reports remainingItemCount on a response it actually
	// paginated, so asking for one object is what makes it report the rest.
	countPageSize = 1
)

// truncatedKey marks a request whose list read stopped short of the cluster's
// full answer. It rides the context rather than being threaded back through
// twenty handlers' own normalisation, because it is a fact about the read rather
// than about any particular list's shape.
const truncatedKey = "kubemg_list_truncated"

// listPage is one page of any Kubernetes list: its items, plus the two metadata
// fields paging and counting are built on.
type listPage[T any] struct {
	Items    []T `json:"items"`
	Metadata struct {
		// Continue is the token for the next page, empty on the last one.
		Continue string `json:"continue"`
		// RemainingItemCount is how many objects the API server says are left
		// after this page. It is a pointer because absent and zero are
		// different answers, and the API server only sets it when it knows.
		RemainingItemCount *int64 `json:"remainingItemCount"`
	} `json:"metadata"`
}

// pagedPath adds paging parameters to a list path. The path is built by this
// package from the fixed inventory, never taken from a caller, so appending to
// its query is safe; a path that already carries one keeps it.
func pagedPath(path string, limit int, cont string) string {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	if cont != "" {
		params.Set("continue", cont)
	}

	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + params.Encode()
}

// noteTruncated records that this request's answer is short of the cluster's.
func noteTruncated(c *gin.Context) {
	c.Set(truncatedKey, true)
}

// listTruncated reports whether any read behind this request stopped short.
func listTruncated(c *gin.Context) bool {
	value, ok := c.Get(truncatedKey)
	if !ok {
		return false
	}
	truncated, _ := value.(bool)
	return truncated
}

// listResponse writes a list payload, adding the truncation note when a read
// behind it stopped short. Every list handler answers through this rather than
// c.JSON directly, so a list that grows past the bound cannot quietly present
// itself as complete.
func listResponse(c *gin.Context, payload gin.H) {
	if listTruncated(c) {
		payload["truncated"] = true
		payload["truncated_at"] = maxListItems
	}
	c.JSON(http.StatusOK, payload)
}

/*
 * walkListPages is the paging loop, and it is a free function over a fetcher
 * seam for the same reason walkEventPages is: its contract can then be pinned
 * without a cluster, and this loop has two rules that are quiet when wrong.
 *
 * An **empty page carrying a continue token is not the end of the list** — the
 * API server returns one whenever its scan skipped a whole page's worth — and
 * treating it as the end produces a confident "nothing here" rather than an
 * error, on exactly the clusters large enough that nobody sees it first.
 *
 * A `continue` token the cluster has since compacted answers **410 Gone**, and
 * that is truncation rather than failure: the pages already read are a real, if
 * partial, answer, and turning a mostly-successful read into an error because
 * its tail expired would be the worst of both. Only a continuation can expire,
 * so a 410 on the opening page is a real error and is handed back as itself.
 *
 * `cont` is empty for a read starting at the first page, and carries a token for
 * one resuming after a caller has already inspected the opening page — which is
 * what the optional-resource and degrading reads below do to settle a 404 or a
 * 403 before paging.
 */
func walkListPages[T any](fetch listPageFetcher, cont string, out *[]T) (pageWalk, bool) {
	first := cont == ""
	for {
		remaining := maxListItems - len(*out)
		if remaining <= 0 {
			return pageWalk{truncated: true}, true
		}

		limit := listPageSize
		if remaining < limit {
			limit = remaining
		}

		status, body, ok := fetch(limit, cont)
		if !ok {
			return pageWalk{}, false
		}
		if status == http.StatusGone && !first {
			return pageWalk{truncated: true}, true
		}
		if status < 200 || status >= 300 {
			return pageWalk{status: status, body: body}, true
		}

		var page listPage[T]
		if err := json.Unmarshal(body, &page); err != nil {
			return pageWalk{unreadable: true}, true
		}
		*out = append(*out, page.Items...)

		cont = page.Metadata.Continue
		first = false
		if cont == "" {
			return pageWalk{}, true
		}
	}
}

// listPageFetcher fetches one page of a list. `ok=false` means the fetch already
// answered the HTTP request and the caller must simply return.
type listPageFetcher func(limit int, cont string) (status int, body []byte, ok bool)

// pageWalk is what a completed walk has to report back to the HTTP layer: the
// cluster's own refusal if it gave one, an unreadable body if it gave that, and
// whether the answer stopped short of the cluster's.
type pageWalk struct {
	truncated  bool
	unreadable bool
	status     int
	body       []byte
}

// render writes whatever a walk found that the caller cannot ignore, and reports
// whether the read succeeded.
func (w pageWalk) render(c *gin.Context) bool {
	if w.truncated {
		noteTruncated(c)
	}
	switch {
	case w.status != 0:
		// The API server's own explanation, exactly as decodeResource hands it
		// back: "forbidden: pods is forbidden for user X" beats anything we
		// would invent.
		c.JSON(w.status, gin.H{"error": kubeErrorMessage(w.body, w.status)})
		return false
	case w.unreadable:
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable response"})
		return false
	}
	return true
}

// pageFetcher wires walkListPages to one proxied path.
func pageFetcher(s *server, c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string,
) listPageFetcher {
	return func(limit int, cont string) (int, []byte, bool) {
		resp, ok := s.callResource(c, user, cluster, grant, pagedPath(path, limit, cont))
		if !ok {
			return 0, nil, false
		}
		return resp.Status, resp.Body, true
	}
}

/*
 * fetchList reads every page of one list path into out, up to maxListItems.
 *
 * It appends rather than assigning, because an all-namespaces read on a scoped
 * grant calls it once per namespace and the results are one list. That also
 * means the bound is on the whole read: a grant covering twenty namespaces does
 * not get twenty times the budget.
 */
func fetchList[T any](s *server, c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string, out *[]T,
) bool {
	walk, ok := walkListPages(pageFetcher(s, c, user, cluster, grant, path), "", out)
	if !ok {
		return false
	}
	return walk.render(c)
}

/*
 * fetchOptionalList reads the first candidate path that answers, then pages it.
 *
 * A 404 from every candidate means the cluster does not serve this resource at
 * all — an uninstalled Gateway API or Istio — which is an answer rather than a
 * failure: the caller reports it as an empty list the UI can label. The version
 * fallback is settled by the *first* page: once a candidate has answered, its
 * continue tokens belong to that version and no other.
 */
func fetchOptionalList[T any](s *server, c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, paths []string, out *[]T,
) (found bool, ok bool) {
	for _, path := range paths {
		resp, callOK := s.callResource(c, user, cluster, grant, pagedPath(path, listPageSize, ""))
		if !callOK {
			return false, false
		}
		if resp.Status == http.StatusNotFound {
			continue
		}

		var page listPage[T]
		if !s.decodeResource(c, resp, &page) {
			return false, false
		}
		*out = append(*out, page.Items...)

		if page.Metadata.Continue == "" {
			return true, true
		}
		walk, walkOK := walkListPages(
			pageFetcher(s, c, user, cluster, grant, path), page.Metadata.Continue, out)
		if !walkOK || !walk.render(c) {
			return false, false
		}
		return true, true
	}
	return false, true
}

/*
 * fetchDegradingList is fetchList over fetchDegrading's contract: a refusal from
 * the cluster's own RBAC narrows the answer rather than failing the request,
 * while anything else is the hard failure it is. Only the opening page can be
 * refused that way — a continuation is reached with the same identity that was
 * already allowed — so the refusal check belongs on that page alone.
 */
func fetchDegradingList[T any](s *server, c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string, out *[]T,
) (available bool, reason string, ok bool) {
	resp, callOK := s.callResource(c, user, cluster, grant, pagedPath(path, listPageSize, ""))
	if !callOK {
		return false, "", false
	}
	if resp.Status == http.StatusForbidden {
		return false, kubeErrorMessage(resp.Body, resp.Status), true
	}

	var page listPage[T]
	if !s.decodeResource(c, resp, &page) {
		return false, "", false
	}
	*out = append(*out, page.Items...)

	if page.Metadata.Continue == "" {
		return true, "", true
	}
	walk, walkOK := walkListPages(
		pageFetcher(s, c, user, cluster, grant, path), page.Metadata.Continue, out)
	if !walkOK || !walk.render(c) {
		return false, "", false
	}
	return true, "", true
}
