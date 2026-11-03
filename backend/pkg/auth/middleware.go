package auth

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// contextKey is the Gin context key holding the verified claims.
const contextKey = "kubemg_claims"

// QueryTokenParam carries a short-lived, single-use ticket (see
// Manager.IssueWSTicket) on a WebSocket upgrade. A browser cannot set headers
// when opening a WebSocket, so this is the only way an in-page terminal can
// authenticate — but the session JWT itself never goes on the wire this way,
// since a query string routinely ends up in proxy access logs and browser
// history.
const QueryTokenParam = "access_token"

// RequireAuth validates the Bearer token and stores its claims on the request
// context.
//
// Two credential shapes arrive here. A session JWT is the console's, and a
// kmg-prefixed opaque token is a machine account's — a CI pipeline holding one
// credential for months, which is exactly the lifetime a stateless token cannot
// be withdrawn over. The second is resolved by the verifier, which reads the
// stored row, so a revoked token stops working on its next call rather than at
// its own expiry. Everything past this point sees one shape of claims.
func RequireAuth(m *Manager, service ...MachineTokenVerifier) gin.HandlerFunc {
	var verifier MachineTokenVerifier
	if len(service) > 0 {
		verifier = service[0]
	}
	return RequireAuthGated(m, verifier, nil)
}

// SessionGate is asked about every verified full session token — never a
// proxy-scoped one (a kubeconfig, a CLI credential) or a machine token. It
// answers false after writing its own refusal.
type SessionGate func(c *gin.Context, claims *Claims) bool

// RequireAuthGated is RequireAuth with a gate on console sessions. It exists
// for the password age policy: a session whose password is due is held to the
// routes that let its owner change it.
func RequireAuthGated(m *Manager, verifier MachineTokenVerifier, gate SessionGate) gin.HandlerFunc {
	return func(c *gin.Context) {
		var (
			claims *Claims
			err    error
		)

		if token, ok := bearerToken(c.GetHeader("Authorization")); ok {
			if strings.HasPrefix(token, CLIRefreshPrefix) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "a kubemg CLI session token is accepted only by the CLI's own routes",
				})
				return
			}
			if IsMachineToken(token) {
				// A build without programmatic access wired refuses these rather
				// than falling through to the JWT parser, which would answer the
				// same 401 by a route that says nothing about why.
				if verifier == nil {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
						"error": "programmatic access is not enabled on this server",
					})
					return
				}
				claims, err = verifier.VerifyMachineToken(c.Request.Context(), token)
			} else {
				claims, err = m.Parse(token)
			}
		} else if m.acceptsWSTicket(c) {
			// A browser cannot set a header when opening a WebSocket, so the
			// upgrade carries a ticket on the query string instead — see
			// QueryTokenParam. The ticket is redeemed once and discarded; it is
			// never the session JWT itself, which a query string would otherwise
			// leave sitting in proxy access logs and browser history.
			ticket := strings.TrimSpace(c.Query(QueryTokenParam))
			if ticket == "" {
				RefuseSession(c, "missing bearer token")
				return
			}
			var redeemed bool
			claims, redeemed = m.redeemWSTicket(c.Request.Context(), ticket)
			if !redeemed {
				RefuseSession(c, "invalid or expired ticket")
				return
			}
		} else {
			RefuseSession(c, "missing bearer token")
			return
		}
		if err != nil || claims == nil {
			RefuseSession(c, "invalid or expired token")
			return
		}
		// A kubeconfig token is a file on someone's laptop. It authenticates
		// kubectl against the one cluster it was issued for and nothing else.
		if claims.Scope == ScopeProxy && !isProxyRequest(c, claims.ClusterID) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "this token may only be used against its cluster's kubectl proxy",
			})
			return
		}

		if gate != nil && claims.Scope == "" && !gate(c, claims) {
			return
		}

		c.Set(contextKey, claims)
		c.Next()
	}
}

// ClaimsFrom retrieves the verified claims from a Gin context.
func ClaimsFrom(c *gin.Context) (*Claims, bool) {
	value, exists := c.Get(contextKey)
	if !exists {
		return nil, false
	}
	claims, ok := value.(*Claims)
	return claims, ok
}

// proxyRoute is the only route a ScopeProxy token may reach. Matching the
// registered route rather than the raw URL keeps the check exact: every other
// route with an ":id" parameter names a different kind of object.
const proxyRoute = "/api/v1/clusters/:id/proxy/*path"

// isProxyRequest reports whether this request is the kubectl proxy for the
// cluster the token was issued for.
func isProxyRequest(c *gin.Context, clusterID uint) bool {
	if c.FullPath() != proxyRoute || clusterID == 0 {
		return false
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	return err == nil && uint(id) == clusterID
}

// isWebSocketUpgrade reports whether this request is opening a WebSocket.
// acceptsWSTicket reports whether this request may authenticate with a ticket
// on its query string: a GET WebSocket handshake, on a route the server named.
// The headers alone were not enough — anyone holding a leaked ticket could add
// Upgrade and Connection to a POST on any route and run it as the ticket's
// owner.
func (m *Manager) acceptsWSTicket(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet || !isWebSocketUpgrade(c.Request) {
		return false
	}
	return len(m.ticketRoutes) == 0 || slices.Contains(m.ticketRoutes, c.FullPath())
}

// SessionInvalidCode marks a 401 that is about the caller's credential itself:
// none, expired, revoked, or an account that no longer exists. It is the
// console's cue to sign out. Every other 401 (a wrong current password, a
// wrong second-factor code, a cluster refusing what the tunnel carried) is an
// answer about one request, and signing out on it threw away a session that
// was fine.
const SessionInvalidCode = "session_invalid"

// RefuseSession answers 401 for a credential that does not authenticate.
func RefuseSession(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message, "code": SessionInvalidCode})
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	// Connection is a comma-separated list, and casing is not guaranteed.
	for _, value := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "upgrade") {
			return true
		}
	}
	return false
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
