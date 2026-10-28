package kubeparams

import (
	"net/url"
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

func TestQuery(t *testing.T) {
	if got := Query("/api/v1/pods"); len(got) != 0 {
		t.Fatalf("a path with no query has no parameters, got %v", got)
	}
	got := Query("/api/v1/pods?watch=t&bad=%zz&limit=5")
	if got.Get("watch") != "t" || got.Get("limit") != "5" {
		t.Fatalf("well-formed pairs must survive a malformed one, got %v", got)
	}
}
