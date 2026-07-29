package api

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/cache"
)

/*
 * Serving a repeated read from memory.
 *
 * Every live read goes down a tunnel, is impersonated at the API server and is
 * written to the audit trail. That is the right price for a question, and the
 * wrong price for the same question three times in as many seconds — which is
 * what a console does: a sidebar click back to a list just left, a drawer
 * opening over the list it came from, a chart re-reading a window nothing has
 * moved in.
 *
 * This is a middleware rather than something inside the handlers on purpose.
 * There are forty resource reads and one caching rule, and the rule is about the
 * request — who is asking, which cluster, which question — not about any
 * particular list's shape. Wrapping the group means a list added later is cached
 * without anybody remembering to do it.
 *
 * What keeps it honest:
 *
 *   - The key carries the caller. Two people with different grants asking the
 *     same question are owed different answers, so `userID` is in the key. It is
 *     the *identity* rather than the resolved grant because a grant is looked up
 *     per request from the same user id; a grant that changes takes effect at
 *     the next miss, at most one TTL away.
 *   - Only a 200 is stored. A refusal is cheap to repeat and expensive to get
 *     wrong: caching a 403 would outlive the grant that fixes it.
 *   - A write invalidates the cluster. Scale, restart, a manifest PUT or a Helm
 *     values write all change what every read of that cluster answers, so the
 *     whole scope is dropped rather than guessing which lists were affected.
 *   - `Cache-Control: no-cache` bypasses the lookup. That is what the console's
 *     Refresh button sends: an operator asking again explicitly is asking the
 *     cluster, not us.
 *
 * An entry lives in this process's memory for seconds and is never written
 * anywhere; a read whose body is sensitive — a Secret's manifest, a Helm
 * release's values — is held on exactly the terms it was already served on, to
 * the one identity that was allowed to ask for it.
 */

const (
	// maxCachedBody is the largest response worth holding. Past this the entry
	// costs more memory than the round trip it saves, and a list that big is
	// already being paginated by the operator picking a namespace.
	maxCachedBody = 2 << 20

	// cacheStatusHeader tells a client — and a test — whether an answer came
	// from memory. It is exposed through CORS so the browser can read it.
	cacheStatusHeader = "X-KubeMG-Cache"
)

// cachedResponse is one stored answer. Only 200s get here, so there is no
// status to keep.
type cachedResponse struct {
	contentType string
	body        []byte
}

// cachedRead serves a repeated GET from memory and drops a cluster's entries
// when a write goes past. With no cache configured it is a pass-through, which
// is how a deployment turns caching off entirely.
func (s *server) cachedRead() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.reads == nil {
			c.Next()
			return
		}

		scope := "cluster:" + c.Param("id")

		if c.Request.Method != http.MethodGet {
			c.Next()
			// A write that the cluster accepted has changed the answers we are
			// holding. It is dropped after the handler rather than before,
			// because a write that was refused changed nothing.
			if c.Writer.Status() >= 200 && c.Writer.Status() < 300 {
				s.reads.InvalidateScope(scope)
			}
			return
		}

		key, ok := s.readCacheKey(c)
		if !ok {
			// No identity on the request: nothing to key an entry to, so this is
			// read live. RequireAuth runs first on every route this wraps, so in
			// practice this is unreachable.
			c.Next()
			return
		}

		if !noCacheRequested(c.Request) {
			if hit, found := s.reads.Get(key); found {
				c.Header(cacheStatusHeader, "hit")
				c.Data(http.StatusOK, hit.contentType, hit.body)
				c.Abort()
				return
			}
		}

		// The handler writes through to the real response as it always did; the
		// recorder keeps a copy alongside, so nothing about the response the
		// caller sees depends on whether it turned out to be cacheable.
		recorder := &responseRecorder{ResponseWriter: c.Writer}
		c.Writer = recorder
		c.Header(cacheStatusHeader, "miss")
		c.Next()
		c.Writer = recorder.ResponseWriter

		if recorder.cacheable() {
			s.reads.Put(scope, key, cachedResponse{
				contentType: recorder.Header().Get("Content-Type"),
				body:        recorder.body.Bytes(),
			})
		}
	}
}

// readCacheKey is the question plus who is asking it: the cluster, the caller
// and their privilege, the exact path — which carries the pod or object name —
// and the query in a canonical order.
func (s *server) readCacheKey(c *gin.Context) (string, bool) {
	claims, ok := auth.ClaimsFrom(c)
	if !ok {
		return "", false
	}
	return cache.Key(
		"v1",
		c.Param("id"),
		strconv.FormatUint(uint64(claims.UserID), 10),
		claims.Role,
		c.Request.URL.Path,
		cache.SortedQuery(c.Request.URL.Query()),
	), true
}

// noCacheRequested reports whether the caller asked to skip the cache. Both
// spellings are honoured: `Cache-Control: no-cache` is what the console sends,
// and `Pragma: no-cache` is what a hard reload in some browsers sends instead.
func noCacheRequested(request *http.Request) bool {
	control := strings.ToLower(request.Header.Get("Cache-Control"))
	if strings.Contains(control, "no-cache") || strings.Contains(control, "no-store") {
		return true
	}
	return strings.Contains(strings.ToLower(request.Header.Get("Pragma")), "no-cache")
}

// responseRecorder tees a handler's response into a buffer. A response past
// maxCachedBody stops being buffered and is marked oversized rather than
// truncated — half a list served as a whole one is worse than no cache.
type responseRecorder struct {
	gin.ResponseWriter
	body     bytes.Buffer
	status   int
	oversize bool
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.record(payload)
	return r.ResponseWriter.Write(payload)
}

func (r *responseRecorder) WriteString(payload string) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.record([]byte(payload))
	return r.ResponseWriter.WriteString(payload)
}

func (r *responseRecorder) record(payload []byte) {
	if r.oversize {
		return
	}
	if r.body.Len()+len(payload) > maxCachedBody {
		r.oversize = true
		r.body.Reset()
		return
	}
	r.body.Write(payload)
}

func (r *responseRecorder) cacheable() bool {
	return r.status == http.StatusOK && !r.oversize && r.body.Len() > 0
}
