package auth

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// contextKey is the Gin context key holding the verified claims.
const contextKey = "kubemg_claims"

// QueryTokenParam carries the session token on a WebSocket upgrade. A browser
// cannot set headers when opening a WebSocket, so this is the only way an
// in-page terminal can authenticate.
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
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			// Fall back to the query parameter, but only for an upgrade: on an
			// ordinary request a token in the URL would end up in proxy logs
			// and browser history for no reason, since a header works there.
			if isWebSocketUpgrade(c.Request) {
				token = strings.TrimSpace(c.Query(QueryTokenParam))
				ok = token != ""
			}
		}
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}

		var (
			claims *Claims
			err    error
		)
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
		if err != nil || claims == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
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

		c.Set(contextKey, claims)
		c.Next()
	}
}

// RequireRole rejects authenticated requests whose role does not match.
// It must be chained after RequireAuth.
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		if claims.Role != role {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient privileges"})
			return
		}
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
