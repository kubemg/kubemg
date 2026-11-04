package kubeparams

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// apiServerReads is what the API server makes of a boolean parameter in a raw
// query: the request's URL.Query(), then the generated conversion, which only
// calls Convert_Slice_string_To_bool when the parameter has a value.
func apiServerReads(t *testing.T, raw, name string) bool {
	t.Helper()
	values, _ := url.ParseQuery(raw)
	var out bool
	if v, ok := values[name]; ok && len(v) > 0 {
		if err := runtime.Convert_Slice_string_To_bool(&v, &out, nil); err != nil {
			t.Fatalf("convert %q: %v", raw, err)
		}
	}
	return out
}

func TestBoolAgreesWithTheAPIServer(t *testing.T) {
	values := []string{"", "0", "1", "true", "false", "TRUE", "FALSE", "True", "False", "t", "f", "yes", "no", "00", " false"}
	for _, name := range []string{"watch", "follow"} {
		raws := []string{
			"",
			"limit=500",
			name,
			// Escapes are decoded before the rule applies.
			name + "=%74rue",
			name + "=f%61lse",
			name + "=+false",
			// Only the first value counts.
			name + "=0&" + name + "=1",
			name + "=1&" + name + "=0",
			// A malformed pair costs only itself.
			name + "=0&bad=%zz",
			"bad=%zz&" + name + "=t",
			"bad;pair&" + name + "=1",
		}
		for _, v := range values {
			raws = append(raws, name+"="+url.QueryEscape(v))
		}
		for _, raw := range raws {
			want := apiServerReads(t, raw, name)
			if got := Bool(Query("/api/v1/pods?"+raw), name); got != want {
				t.Errorf("%s in %q: Bool = %v, the API server reads %v", name, raw, got, want)
			}
		}
	}
}

// filler returns a query of n parameters, the first of them watch=true.
func filler(n int) string {
	return "watch=true" + strings.Repeat("&x=1", n-1)
}

func TestTooManyParams(t *testing.T) {
	if TooManyParams("/api/v1/pods") {
		t.Fatal("a path with no query is never over the limit")
	}
	if TooManyParams("/api/v1/pods?" + filler(MaxParams)) {
		t.Fatalf("%d parameters is at the limit, not past it", MaxParams)
	}
	if !TooManyParams("/api/v1/pods?" + filler(MaxParams+1)) {
		t.Fatalf("%d parameters is past the limit", MaxParams+1)
	}
}

// MaxParams has to be the limit net/url actually applies: at it, the watch is
// still read; one past it, url.ParseQuery reads nothing. A Go that moves the
// limit fails here rather than leaving TooManyParams out of step.
func TestMaxParamsIsNetURLsLimit(t *testing.T) {
	at, err := url.ParseQuery(filler(MaxParams))
	if err != nil || at.Get("watch") != "true" {
		t.Fatalf("at the limit url.ParseQuery should read the query, got err %v", err)
	}
	past, err := url.ParseQuery(filler(MaxParams + 1))
	if err == nil || len(past) != 0 {
		t.Fatalf("past the limit url.ParseQuery should read nothing, got %d keys and err %v", len(past), err)
	}
	if Bool(Query("/api/v1/pods?"+filler(MaxParams+1)), "watch") {
		t.Fatal("past the limit the watch is not readable, which is why the gateway refuses it")
	}
}

// GODEBUG=urlmaxqueryparams moves net/url's limit, and a custom limit compares
// with < where the default compares with <=. TooManyParams asks net/url, so it
// agrees with url.ParseQuery on either side of whatever the limit is.
func TestTooManyParamsFollowsACustomLimit(t *testing.T) {
	t.Setenv("GODEBUG", "urlmaxqueryparams=100")
	for n := 98; n <= 101; n++ {
		raw := filler(n)
		_, err := url.ParseQuery(raw)
		if got, want := TooManyParams("/api/v1/pods?"+raw), err != nil; got != want {
			t.Errorf("%d parameters under a limit of 100: TooManyParams = %v, url.ParseQuery refuses = %v", n, got, want)
		}
	}
	if !TooManyParams("/api/v1/pods?" + filler(100)) {
		t.Fatal("a custom limit of 100 refuses 100 parameters; the probe did not see the GODEBUG setting")
	}
}

// Within the limit QueryPastLimit reads exactly what Query reads. Every query
// of up to five characters from an alphabet of the characters that matter is
// compared with url.ParseQuery: separators, escapes good and bad, the
// semicolon it refuses, and plain characters.
func TestQueryPastLimitAgreesWithQuery(t *testing.T) {
	alphabet := []string{"a", "=", "&", ";", "%", "2", "z", "+"}
	queries := []string{""}
	for length := 1; length <= 5; length++ {
		next := make([]string, 0, len(queries)*len(alphabet))
		for _, prefix := range queries {
			if len(prefix) != length-1 {
				continue
			}
			for _, char := range alphabet {
				next = append(next, prefix+char)
			}
		}
		queries = append(queries, next...)
	}
	for _, raw := range queries {
		want := Query("/api/v1/pods?" + raw)
		if got := QueryPastLimit("/api/v1/pods?" + raw); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: QueryPastLimit = %v, Query = %v", raw, got, want)
		}
	}
	if got := QueryPastLimit("/api/v1/pods"); len(got) != 0 {
		t.Fatalf("a path with no query has no parameters, got %v", got)
	}
}

// Past the limit Query reads nothing, and QueryPastLimit still reads the call.
func TestQueryPastLimitReadsPastTheLimit(t *testing.T) {
	path := "/api/v1/pods?" + filler(MaxParams+1)
	if len(Query(path)) != 0 {
		t.Fatal("past the limit Query should read nothing")
	}
	if !Bool(QueryPastLimit(path), "watch") {
		t.Fatal("past the limit QueryPastLimit should still read the watch")
	}
}

func TestQuery(t *testing.T) {
	if got := Query("/api/v1/pods"); len(got) != 0 {
		t.Fatalf("a path with no query has no parameters, got %v", got)
	}
	got := Query("/api/v1/pods?watch=t&bad=%zz&limit=5")
	if got.Get("watch") != "t" || got.Get("limit") != "5" {
		t.Fatalf("well-formed pairs must survive a malformed one, got %v", got)
	}
}

// SelectedName follows the request-info resolver: the query decoded as
// ListOptions, an exact metadata.name term, and a name that could be a path
// segment. What it names is what RBAC sees as the request's name.
func TestSelectedName(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		// kubectl get pod web-0 -w, and the same read without the watch.
		{"fieldSelector=metadata.name%3Dweb-0&watch=true", "web-0"},
		{"fieldSelector=metadata.name%3Dweb-0", "web-0"},
		{"fieldSelector=metadata.name%3D%3Dweb-0", "web-0"},
		// An exact name term among others still names the object.
		{"fieldSelector=status.phase%3DRunning%2Cmetadata.name%3Dweb-0", "web-0"},
		{"fieldSelector=metadata.name%3Dweb-0&labelSelector=app%3Dweb&limit=5", "web-0"},
		// Anything short of an exact match names nothing.
		{"fieldSelector=metadata.name%21%3Dweb-0", ""},
		{"fieldSelector=status.phase%3DRunning", ""},
		{"fieldSelector=metadata.namespace%3Dteam-a", ""},
		{"labelSelector=metadata.name%3Dweb-0", ""},
		{"watch=true", ""},
		{"", ""},
		// A name that could not be a path segment is not taken.
		{"fieldSelector=metadata.name%3D..", ""},
		{"fieldSelector=metadata.name%3Da%2Fb", ""},
		{"fieldSelector=metadata.name%3Da%25b", ""},
		// The selector has to parse, and so does every other list option: the
		// resolver names nothing when any of them fails to decode.
		{"fieldSelector=metadata.name", ""},
		{"fieldSelector=metadata.name%3Dweb-0&limit=many", ""},
		{"fieldSelector=metadata.name%3Dweb-0&timeoutSeconds=soon", ""},
		// A malformed pair is dropped before decoding, as the API server's
		// URL.Query() drops it.
		{"bad=%zz&fieldSelector=metadata.name%3Dweb-0", "web-0"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			if got := SelectedName(Query("/api/v1/namespaces/team-a/pods?" + tc.query)); got != tc.want {
				t.Fatalf("SelectedName(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}
