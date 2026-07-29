package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/cache"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The read cache stands in front of forty live reads, so what is pinned here is
 * the middleware's rules rather than any one list: a repeated read is served
 * from memory, a *different caller's* identical read is not, a refusal is never
 * stored, a write drops the cluster's entries, and Cache-Control: no-cache
 * reaches the cluster.
 *
 * Each case drives the middleware over a handler that counts its calls, because
 * the whole point of the cache is how many times the thing behind it runs.
 */

type cacheProbe struct {
	router *gin.Engine
	jwt    *auth.Manager
	// calls counts how many times the handler behind the cache ran.
	calls int
}

// newCacheProbe wires the real middleware, behind the real auth middleware, in
// front of a counting handler on the same route shape the resource group uses.
func newCacheProbe(t *testing.T, ttl time.Duration, respond gin.HandlerFunc) *cacheProbe {
	t.Helper()

	manager := authManagerForTest()
	s := &server{jwt: manager}
	if ttl >= 0 {
		s.reads = cache.New[cachedResponse](ttl)
	}

	probe := &cacheProbe{jwt: manager}
	router := gin.New()
	group := router.Group("/api/v1/clusters/:id/resources", auth.RequireAuth(manager), s.cachedRead())
	count := func(c *gin.Context) {
		probe.calls++
		respond(c)
	}
	group.GET("/pods", count)
	group.POST("/scale", count)

	probe.router = router
	return probe
}

func (p *cacheProbe) tokenFor(t *testing.T, userID uint) string {
	t.Helper()
	token, _, err := p.jwt.Generate(userID, fmt.Sprintf("user-%d", userID), db.RoleUser)
	if err != nil {
		t.Fatalf("signing a token: %v", err)
	}
	return token
}

func (p *cacheProbe) get(t *testing.T, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	return rec
}

func okPods(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"pods": []string{"api-0"}})
}

func TestCachedReadServesARepeatedRead(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, okPods)
	token := probe.tokenFor(t, 7)

	first := probe.get(t, "/api/v1/clusters/3/resources/pods?namespace=shop", token, nil)
	second := probe.get(t, "/api/v1/clusters/3/resources/pods?namespace=shop", token, nil)

	if probe.calls != 1 {
		t.Fatalf("the cluster was read %d times; want 1", probe.calls)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("cached body = %q, want %q", second.Body.String(), first.Body.String())
	}
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Fatalf("%s = %q on the second read, want hit", cacheStatusHeader, got)
	}
	if got := second.Header().Get("Content-Type"); got == "" {
		t.Fatal("a cached answer came back with no content type")
	}
}

// The query is part of the question. Sharing an entry across namespaces would
// answer one namespace's list with another's.
func TestCachedReadKeysOnTheQuery(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, okPods)
	token := probe.tokenFor(t, 7)

	probe.get(t, "/api/v1/clusters/3/resources/pods?namespace=shop", token, nil)
	probe.get(t, "/api/v1/clusters/3/resources/pods?namespace=team-a", token, nil)
	probe.get(t, "/api/v1/clusters/4/resources/pods?namespace=shop", token, nil)

	if probe.calls != 3 {
		t.Fatalf("the cluster was read %d times; want 3 distinct questions", probe.calls)
	}
}

// The one rule that matters most: an entry belongs to the identity that asked
// for it, because two grants are owed two answers.
func TestCachedReadNeverCrossesCallers(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, okPods)

	probe.get(t, "/api/v1/clusters/3/resources/pods", probe.tokenFor(t, 7), nil)
	rec := probe.get(t, "/api/v1/clusters/3/resources/pods", probe.tokenFor(t, 8), nil)

	if probe.calls != 2 {
		t.Fatalf("the cluster was read %d times; want one read per caller", probe.calls)
	}
	if got := rec.Header().Get(cacheStatusHeader); got != "miss" {
		t.Fatalf("%s = %q for a second caller, want miss", cacheStatusHeader, got)
	}
}

// Refresh in the console sends this, and it has to reach the cluster: an
// operator asking again explicitly is asking the cluster, not us.
func TestCachedReadBypassedByNoCache(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, okPods)
	token := probe.tokenFor(t, 7)

	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	probe.get(t, "/api/v1/clusters/3/resources/pods", token,
		map[string]string{"Cache-Control": "no-cache"})
	probe.get(t, "/api/v1/clusters/3/resources/pods", token,
		map[string]string{"Pragma": "no-cache"})

	if probe.calls != 3 {
		t.Fatalf("the cluster was read %d times; want every no-cache read to reach it", probe.calls)
	}
}

// A refusal is cheap to repeat and expensive to hold: caching a 403 would
// outlive the grant that fixes it.
func TestCachedReadDoesNotStoreRefusals(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, func(c *gin.Context) {
		c.JSON(http.StatusForbidden, gin.H{"error": "namespace is outside your granted scope"})
	})
	token := probe.tokenFor(t, 7)

	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	rec := probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)

	if probe.calls != 2 {
		t.Fatalf("the cluster was read %d times; want a refusal to be re-asked", probe.calls)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want the refusal to come back unchanged", rec.Code)
	}
}

// A scale or a restart changes what every list of that cluster answers, so the
// next read has to reach the cluster — otherwise the console looks like the
// write did not land.
func TestWriteInvalidatesTheClustersReads(t *testing.T) {
	probe := newCacheProbe(t, time.Minute, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	token := probe.tokenFor(t, 7)

	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	if probe.calls != 1 {
		t.Fatalf("the cluster was read %d times before the write; want 1", probe.calls)
	}

	write := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/3/resources/scale", nil)
	write.Header.Set("Authorization", "Bearer "+token)
	probe.router.ServeHTTP(httptest.NewRecorder(), write)

	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	if probe.calls != 3 {
		t.Fatalf("handler calls = %d; want the write to have dropped the cached list", probe.calls)
	}
}

// A negative TTL is how a deployment turns caching off; nothing may be held.
func TestCachingCanBeDisabled(t *testing.T) {
	probe := newCacheProbe(t, -1, okPods)
	token := probe.tokenFor(t, 7)

	probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)
	rec := probe.get(t, "/api/v1/clusters/3/resources/pods", token, nil)

	if probe.calls != 2 {
		t.Fatalf("the cluster was read %d times with caching off; want 2", probe.calls)
	}
	if got := rec.Header().Get(cacheStatusHeader); got != "" {
		t.Fatalf("%s = %q with caching off, want no header at all", cacheStatusHeader, got)
	}
}

// The console has to be able to send the header the bypass depends on, and to
// read the one that says how it was answered.
func TestPreflightAllowsCacheControl(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/clusters", nil)
	req.Header.Set("Origin", devOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "authorization,cache-control")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	allowed := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, "cache-control") {
		t.Fatalf("allow-headers = %q, want cache-control", allowed)
	}

	// Expose-Headers is asserted on a real cross-origin response rather than on
	// the preflight: that is where it belongs and where the middleware writes it.
	// A preflight answers what the browser may *send*; what it may *read* is
	// declared on the answer it reads.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", devOrigin)
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	exposed := strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers"))
	if !strings.Contains(exposed, strings.ToLower(cacheStatusHeader)) {
		t.Fatalf("expose-headers = %q, want %s", exposed, cacheStatusHeader)
	}
}
