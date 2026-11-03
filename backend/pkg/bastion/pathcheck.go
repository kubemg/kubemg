package bastion

import (
	"net/http"
	"strings"
)

// ambiguousPath reports why a path cannot be read the way the API server will
// read it, or "" when it can.
//
// Every decision the gateway makes about a call — the namespace it is scoped
// to, whether it is discovery, which guardrail applies, what the trail
// records — comes from ParsePath reading the path in hand. The agent hands
// that path to the API server byte for byte, and the API server decodes it
// once more before it routes. So a path that still carries a percent escape
// here is not the path the cluster acts on: `%2F` becomes a slash and moves
// every segment after it, `%2D` turns a namespace the grant does not name
// into one it does not see. Dot segments and empty segments are the same
// problem from the other side — ParsePath drops what the API server keeps.
//
// No Kubernetes object name can hold '%', '/', "." or ".." (path segment
// names refuse them), so nothing a client legitimately addresses needs any of
// these. The call is refused rather than normalised: a gateway that guesses
// which reading the cluster will take is the defect, not the fix.
//
// Past a proxy subresource the rest of the path belongs to the pod, Service or
// node being proxied to, not to the API server's routing, so it is not
// checked. The segments that address the proxied object still are.
func ambiguousPath(path string) string {
	// The agent sends the path as a string it parses again, so a '#' anywhere
	// in it would start a fragment and silently cut what follows: the
	// cluster would act on a shorter path than the one checked here.
	if strings.ContainsRune(path, '#') {
		return "the path carries a '#', which would cut it short before the API server reads it"
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == "/" {
		return ""
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// One trailing slash is how discovery roots are sometimes asked for and
	// changes nothing about the address.
	if n := len(segments); n > 1 && segments[n-1] == "" {
		segments = segments[:n-1]
	}

	checked := segments
	for i, segment := range segments {
		if segment == "proxy" && ParsePath("/"+strings.Join(segments[:i+1], "/")).Subresource == "proxy" {
			checked = segments[:i+1]
			break
		}
	}

	for _, segment := range checked {
		switch {
		case segment == "":
			return "the path has an empty segment, which kubemg and the API server would read differently"
		case segment == "." || segment == "..":
			return "the path has a dot segment, which kubemg and the API server would read differently"
		case strings.ContainsRune(segment, '%'):
			return "the path carries a percent-encoded character, which the API server decodes again; " +
				"kubemg cannot tell what it addresses"
		case strings.ContainsFunc(segment, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }):
			return "the path carries a character no Kubernetes path holds"
		}
	}
	return ""
}

// readOnlyMethod is what discovery is asked with. The discovery exception lets
// a scoped grant past the namespace check, so it stays exactly as wide as
// kubectl needs it — reading — and no wider.
func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// decodedDelimiter reports why a client's path — as the router decoded it,
// before KubeMG appends the query — cannot be forwarded. A '?' in it arrived
// as %3F: the agent would read it as the start of the query, so the path the
// cluster routes on and the query it reads would both differ from what was
// checked. '#' (from %23) is ambiguousPath's to refuse on every path.
func decodedDelimiter(path string) string {
	if strings.ContainsRune(path, '?') {
		return "the path carries an encoded '?', which the API server would read as the start of the query"
	}
	return ""
}
