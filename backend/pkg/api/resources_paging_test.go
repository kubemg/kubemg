package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The paging loop is what stands between the console and the agent's 8 MB
// ceiling, and both of its rules are quiet when they are wrong: an empty page
// with a continue token read as the end of a list produces a confident "nothing
// here", and an unbounded walk produces a refusal from the tunnel rather than a
// list. So the loop is pinned here, over the same fetcher seam walkEventPages
// uses, rather than only through a cluster nobody has in a unit test.

// pageBody renders one page of a Kubernetes list: `count` items, and a continue
// token when there is more.
func pageBody(t *testing.T, count int, cont string) []byte {
	t.Helper()
	items := make([]map[string]string, 0, count)
	for i := range count {
		items = append(items, map[string]string{"name": "item-" + itoa(uint(i))})
	}
	payload := map[string]any{"items": items}
	if cont != "" {
		payload["metadata"] = map[string]any{"continue": cont}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal page: %v", err)
	}
	return body
}

type namedItem struct {
	Name string `json:"name"`
}

func TestWalkListPagesReadsEveryPage(t *testing.T) {
	pages := [][]byte{
		pageBody(t, 250, "token-1"),
		pageBody(t, 250, "token-2"),
		pageBody(t, 40, ""),
	}
	var asked []string
	call := 0

	var out []namedItem
	walk, ok := walkListPages(func(limit int, cont string) (int, []byte, bool) {
		asked = append(asked, cont)
		if limit != listPageSize {
			t.Fatalf("page %d asked for limit %d, want %d", call, limit, listPageSize)
		}
		body := pages[call]
		call++
		return http.StatusOK, body, true
	}, "", &out)

	if !ok {
		t.Fatal("walk reported the request already answered")
	}
	if walk.truncated {
		t.Fatal("a list that ended on its own must not be marked truncated")
	}
	if len(out) != 540 {
		t.Fatalf("collected %d items, want 540", len(out))
	}
	// The continue token from each page has to travel to the next request, or
	// the loop silently re-reads page one forever.
	if got := strings.Join(asked, ","); got != ",token-1,token-2" {
		t.Fatalf("continue tokens asked = %q, want the empty first then each token", got)
	}
}

// The rule most worth pinning: the API server returns an empty page with a
// continue token whenever its scan skipped a page's worth of objects, and
// reading that as the end of the collection answers "none" for a cluster that
// has plenty.
func TestWalkListPagesDoesNotStopOnAnEmptyPageWithAToken(t *testing.T) {
	pages := [][]byte{
		pageBody(t, 0, "token-1"),
		pageBody(t, 0, "token-2"),
		pageBody(t, 3, ""),
	}
	call := 0

	var out []namedItem
	walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
		body := pages[call]
		call++
		return http.StatusOK, body, true
	}, "", &out)

	if !ok || walk.truncated {
		t.Fatalf("ok = %v, truncated = %v, want a clean complete read", ok, walk.truncated)
	}
	if len(out) != 3 {
		t.Fatalf("collected %d items, want the 3 behind the empty pages", len(out))
	}
}

func TestWalkListPagesStopsAtTheBudgetAndSaysSo(t *testing.T) {
	var out []namedItem
	walk, ok := walkListPages(func(limit int, _ string) (int, []byte, bool) {
		// Always more to give: only the budget can end this walk.
		return http.StatusOK, pageBody(t, limit, "more"), true
	}, "", &out)

	if !ok {
		t.Fatal("walk reported the request already answered")
	}
	if !walk.truncated {
		t.Fatal("a walk stopped by the budget must report truncation")
	}
	if len(out) != maxListItems {
		t.Fatalf("collected %d items, want exactly the budget of %d", len(out), maxListItems)
	}
}

// A continue token expires with the cluster's etcd compaction. The pages already
// read are a real answer, so this is truncation rather than a failed list — but
// a 410 on the *opening* page cannot be an expired token and is a real error.
func TestWalkListPagesTreatsAnExpiredTokenAsTruncation(t *testing.T) {
	t.Run("on a continuation", func(t *testing.T) {
		call := 0
		var out []namedItem
		walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
			call++
			if call == 1 {
				return http.StatusOK, pageBody(t, 10, "token-1"), true
			}
			return http.StatusGone, []byte(`{"message":"continue expired"}`), true
		}, "", &out)

		if !ok || !walk.truncated {
			t.Fatalf("ok = %v, truncated = %v, want a truncated but successful read", ok, walk.truncated)
		}
		if walk.status != 0 {
			t.Fatalf("status = %d, want the expired tail not to fail the read", walk.status)
		}
		if len(out) != 10 {
			t.Fatalf("collected %d items, want the 10 read before the token expired", len(out))
		}
	})

	t.Run("on the opening page", func(t *testing.T) {
		var out []namedItem
		walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
			return http.StatusGone, []byte(`{"message":"gone"}`), true
		}, "", &out)

		if !ok {
			t.Fatal("walk reported the request already answered")
		}
		if walk.status != http.StatusGone {
			t.Fatalf("status = %d, want %d handed back as a real error",
				walk.status, http.StatusGone)
		}
	})

	// A walk resuming after a caller has already read the opening page starts
	// with a token, so its very first 410 is an expired continuation.
	t.Run("on the first page of a resumed walk", func(t *testing.T) {
		var out []namedItem
		walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
			return http.StatusGone, []byte(`{"message":"gone"}`), true
		}, "token-1", &out)

		if !ok || !walk.truncated || walk.status != 0 {
			t.Fatalf("ok = %v, truncated = %v, status = %d, want truncation",
				ok, walk.truncated, walk.status)
		}
	})
}

func TestWalkListPagesHandsBackARefusal(t *testing.T) {
	var out []namedItem
	walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
		return http.StatusForbidden, []byte(`{"message":"pods is forbidden"}`), true
	}, "", &out)

	if !ok {
		t.Fatal("walk reported the request already answered")
	}
	if walk.status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", walk.status, http.StatusForbidden)
	}
}

func TestWalkListPagesReportsAnUnreadableBody(t *testing.T) {
	var out []namedItem
	walk, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
		return http.StatusOK, []byte("not json"), true
	}, "", &out)

	if !ok || !walk.unreadable {
		t.Fatalf("ok = %v, unreadable = %v, want an unreadable body reported", ok, walk.unreadable)
	}
}

// A fetch that already wrote the HTTP response stops the walk without the walk
// writing a second one.
func TestWalkListPagesStopsWhenTheFetchAnswered(t *testing.T) {
	var out []namedItem
	if _, ok := walkListPages(func(_ int, _ string) (int, []byte, bool) {
		return 0, nil, false
	}, "", &out); ok {
		t.Fatal("walk continued after the fetch answered the request")
	}
}

/* ------------------------------------------------------------- paths --- */

func TestPagedPath(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		limit int
		cont  string
		want  string
	}{
		{"first page", "/api/v1/pods", 250, "", "/api/v1/pods?limit=250"},
		{"continuation", "/api/v1/pods", 250, "abc", "/api/v1/pods?continue=abc&limit=250"},
		{"count", "/api/v1/pods", 1, "", "/api/v1/pods?limit=1"},
		// A path that already carries a query keeps it: the capacity read
		// selects schedulable pods with a fieldSelector, and dropping it would
		// turn a narrow read into a cluster-wide one.
		{
			"keeps an existing query",
			"/api/v1/pods?fieldSelector=status.phase%3DRunning",
			250, "",
			"/api/v1/pods?fieldSelector=status.phase%3DRunning&limit=250",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pagedPath(tc.path, tc.limit, tc.cont); got != tc.want {
				t.Fatalf("pagedPath = %q, want %q", got, tc.want)
			}
		})
	}
}

/* -------------------------------------------------------- the response --- */

// Truncation has to reach the client, or a short list presents itself as the
// whole cluster.
func TestListResponseMarksTruncation(t *testing.T) {
	t.Run("silent when the read was complete", func(t *testing.T) {
		c, rec := testContext()
		listResponse(c, gin.H{"pods": []string{}})

		body := decodeMap(t, rec)
		if _, marked := body["truncated"]; marked {
			t.Fatal("a complete read must not be marked truncated")
		}
	})

	t.Run("marked when a read stopped short", func(t *testing.T) {
		c, rec := testContext()
		noteTruncated(c)
		listResponse(c, gin.H{"pods": []string{}})

		body := decodeMap(t, rec)
		if body["truncated"] != true {
			t.Fatalf("truncated = %v, want true", body["truncated"])
		}
		// The bound travels with the flag: "some rows are missing" is not
		// actionable, "the first 2000" is.
		if body["truncated_at"] != float64(maxListItems) {
			t.Fatalf("truncated_at = %v, want %d", body["truncated_at"], maxListItems)
		}
	})
}

func testContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c, rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return body
}
