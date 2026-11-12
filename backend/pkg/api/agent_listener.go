package api

import (
	"net/http"
	"regexp"
)

// agentTunnelPath is the route agents dial, on either listener.
const agentTunnelPath = "/agent/v1/tunnel"

// installDownload matches the two install downloads, the only other thing a
// cluster's side fetches. The ticket segment is matched whole, so no path that
// climbs out of /install/ passes.
var installDownload = regexp.MustCompile(`^/install/[^/]+/(agent\.yaml|kustomize\.tar\.gz)$`)

// AgentListenerHandler is the router as the agent listener serves it: the
// tunnel and the install downloads, and a 404 for everything else, sign-in,
// API, kubectl proxy and console included. Both are credential-gated on their
// own, by the registration token and the single-use download ticket, and
// neither reads the Host it was reached on.
//
// It filters in front of the router rather than mounting a second one, so the
// two routes behave on this listener exactly as they do on the main one.
func AgentListenerHandler(router http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.URL.Path != agentTunnelPath && !installDownload.MatchString(r.URL.Path)) {
			http.NotFound(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
}
