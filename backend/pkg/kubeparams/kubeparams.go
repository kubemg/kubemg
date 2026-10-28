// Package kubeparams reads Kubernetes API query parameters the way the API
// server reads them. Whatever has to predict what a call will do before the
// cluster sees it — whether it streams, which verb to record it under — has to
// decide exactly as the cluster will, or a spelling the cluster accepts slips
// past the prediction.
package kubeparams

import (
	"net/url"
	"strings"
)

// Query decodes the query string of a request path, or of a request URI, as the
// API server decodes its own request's: a malformed pair is skipped rather
// than failing the whole query.
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
