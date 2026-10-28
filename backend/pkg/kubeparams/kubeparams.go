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
