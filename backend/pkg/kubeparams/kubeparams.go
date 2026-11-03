// Package kubeparams reads Kubernetes API query parameters the way the API
// server reads them. Whatever has to predict what a call will do before the
// cluster sees it — whether it streams, which verb to record it under — has to
// decide exactly as the cluster will, or a spelling the cluster accepts slips
// past the prediction.
package kubeparams

import (
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/api/validate/content"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metainternalversionscheme "k8s.io/apimachinery/pkg/apis/meta/internalversion/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MaxParams is net/url's default limit on the parameters of one query. Past
// it, url.ParseQuery reads nothing at all.
const MaxParams = 10000

// TooManyParams reports whether a path's query is past MaxParams, where Query
// can no longer say what the API server will make of it. An API server built
// on a Go with the same limit reads it as empty too; one built before the
// limit reads every parameter. A caller that has to predict the call cannot
// tell which, so it should refuse the call rather than guess.
func TooManyParams(path string) bool {
	_, raw, ok := strings.Cut(path, "?")
	return ok && strings.Count(raw, "&")+1 > MaxParams
}

// Query decodes the query string of a request path, or of a request URI, as the
// API server decodes its own request's: a malformed pair is skipped rather
// than failing the whole query. A query past MaxParams decodes as empty; see
// TooManyParams.
func Query(path string) url.Values {
	_, raw, ok := strings.Cut(path, "?")
	if !ok {
		return url.Values{}
	}
	values, _ := url.ParseQuery(raw)
	return values
}

// Bool reports a boolean parameter by the API server's rule
// (runtime.Convert_Slice_string_To_bool): only an absent parameter, `0` or
// `false` in any case is false. Every other value is true — `t`, `True`, `yes`
// and an empty `?watch=` alike — and only the first value counts.
func Bool(query url.Values, name string) bool {
	values := query[name]
	if len(values) == 0 {
		return false
	}
	return values[0] != "0" && !strings.EqualFold(values[0], "false")
}

// SelectedName is the object a list or watch addresses through its field
// selector, read as the API server's request-info resolver reads it: the
// query decoded as ListOptions, then an exact metadata.name term, kept only if
// it could be a path segment. RBAC sees that name, so a resourceNames rule
// applies to the call, and it is how kubectl watches one object
// (`kubectl get pod web-0 -w` sends fieldSelector=metadata.name=web-0). A query
// any of whose list options fails to decode names nothing, as it names nothing
// to the resolver.
func SelectedName(query url.Values) string {
	var opts metainternalversion.ListOptions
	if err := metainternalversionscheme.ParameterCodec.DecodeParameters(query, metav1.SchemeGroupVersion, &opts); err != nil {
		return ""
	}
	if opts.FieldSelector == nil {
		return ""
	}
	name, ok := opts.FieldSelector.RequiresExactMatch("metadata.name")
	if !ok || len(content.IsPathSegmentName(name)) > 0 {
		return ""
	}
	return name
}
