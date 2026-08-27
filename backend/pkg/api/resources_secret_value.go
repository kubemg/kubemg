package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Revealing one Secret value.
 *
 * Every other read in this product returns a Secret's *keys* and nothing else,
 * and that contract does not change here: the list route still decodes values
 * off the wire and drops them, so no value lands in a browser cache because
 * somebody opened a namespace. What was missing is the routine question — "is
 * this the right value" — which the console could not answer at all, so an
 * operator dropped to `kubectl get secret -o jsonpath` and the reveal happened
 * with no record anywhere.
 *
 * This is the one route in the resource API that *adds an exposure* rather than
 * a reading, so it is built with its guards rather than added quietly:
 *
 *   - It is one key of one Secret, named in the request. There is no route that
 *     returns a whole Secret, and none that returns more than one.
 *   - It needs a capability of its own (db.User.CanRevealSecrets), grantable
 *     only by a super admin, *and* the cluster's own RBAC on the impersonated
 *     read. Neither alone is enough.
 *   - It is audited under a verb of its own with the key named, written before
 *     the bytes go out, and auditpolicy cannot suppress it.
 *   - Two kinds of Secret are refused outright: a ServiceAccount token, which is
 *     a live credential for the cluster itself rather than a value a human put
 *     there, and KubeMG's own agent registration secret, because a console that
 *     will hand out the token its own tunnel authenticates with is a console
 *     that can be talked into minting a second bastion.
 *   - Nothing caches it. The route sits outside the cached resource group, and
 *     the response says so to every layer between here and the browser.
 */

// verbSecretReveal is this server's own verb for the reveal. It is not a
// Kubernetes verb — the proxied `get` underneath is recorded separately, and
// deliberately: one row says a Secret object was read, this one says a named
// value was shown to a person.
const verbSecretReveal = "secret-reveal"

// secretValueLimit bounds what one reveal will hand back. A Secret is capped at
// a megabyte by the API server, so this is a second line rather than the
// primary one — but a value big enough to be worth streaming is not a value
// anybody is eyeballing in a console.
const secretValueLimit = 1 << 20

var (
	secretObjectName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	secretKeyName    = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

// serviceAccountTokenType is the Secret type the cluster mints a ServiceAccount
// bearer token into.
const serviceAccountTokenType = "kubernetes.io/service-account-token"

// agentSecretName is the Secret the agent's install manifests create. Its
// namespace is whatever the effective settings say the agent installs into.
const agentSecretName = "kubemg-agent"

type secretValueResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Type      string `json:"type,omitempty"`
	// Value is the decoded value when it is text. A value that is not valid
	// UTF-8 is not mangled into a string: Encoded carries it base64 exactly as
	// the cluster stores it and Binary says which field to read, because a
	// console that renders a TLS key's DER as replacement characters is worse
	// than one that says it is binary.
	Value   string `json:"value,omitempty"`
	Encoded string `json:"encoded,omitempty"`
	Binary  bool   `json:"binary"`
	Bytes   int    `json:"bytes"`
}

// revealSecretValue returns one key of one Secret.
func (s *server) revealSecretValue(c *gin.Context) {
	started := time.Now()

	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	// Nothing is cached, at any layer, including the refusals: a 403 that a
	// proxy holds onto would outlive the grant that fixes it.
	c.Header("Cache-Control", "no-store")

	name := strings.TrimSpace(c.Query("name"))
	key := strings.TrimSpace(c.Query("key"))
	if name == "" || key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a secret name and a key are required"})
		return
	}
	if !secretObjectName.MatchString(name) || len(name) > 253 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not a valid secret name"})
		return
	}
	if !secretKeyName.MatchString(key) || len(key) > 253 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not a valid secret key"})
		return
	}

	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	// The capability is checked before anything is read, but recorded all the
	// same: an attempt to read a credential by an account that may not is
	// exactly the line an auditor is looking for, and a refusal that leaves no
	// trace makes the capability unanswerable.
	if !user.MayRevealSecrets() {
		s.recordSecretReveal(c, user, cluster, namespace,
			http.StatusForbidden, "caller may not reveal secret values", started)
		c.JSON(http.StatusForbidden, gin.H{
			"error": "you are not allowed to reveal secret values. " +
				"A super admin grants this separately from any role.",
		})
		return
	}

	if name == agentSecretName && namespace == s.settings(c.Request.Context()).AgentNamespace {
		s.recordSecretReveal(c, user, cluster, namespace,
			http.StatusForbidden, "agent registration secret", started)
		c.JSON(http.StatusForbidden, gin.H{
			"error": "this is KubeMG's own agent registration secret, which is never revealed here",
		})
		return
	}

	var object secretObject
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", namespace, name)
	resp, ok := s.callResource(c, user, cluster, grant, path)
	if !ok {
		// callResource has already written the transport failure or the
		// bastion's own refusal; it is recorded by the proxy as the `get` it is.
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		message := kubeErrorMessage(resp.Body, resp.Status)
		s.recordSecretReveal(c, user, cluster, namespace, resp.Status, message, started)
		c.JSON(resp.Status, gin.H{"error": message})
		return
	}
	if !s.decodeResource(c, resp, &object) {
		return
	}

	out, refusal := revealValue(object, namespace, name, key)
	if refusal != nil {
		s.recordSecretReveal(c, user, cluster, namespace, refusal.Status, refusal.Reason, started)
		c.JSON(refusal.Status, gin.H{"error": refusal.Message})
		return
	}

	// Recorded before the value is written, not after. If this process dies
	// between the two, the trail says a reveal happened and the browser never
	// got it — which is the safe way round for the record to be wrong.
	s.recordSecretReveal(c, user, cluster, namespace, http.StatusOK, "", started)
	c.JSON(http.StatusOK, out)
}

// secretObject is the slice of a Kubernetes Secret this route reads. Nothing
// else about the object is fetched and nothing else is kept: no labels, no
// annotations, no second key.
type secretObject struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

// revealRefusal is a reason not to hand a value over. Reason is what the audit
// record says; Message is what the caller reads, and the two are different on
// purpose — the trail wants a short constant to filter on, the operator wants a
// sentence.
type revealRefusal struct {
	Status  int
	Reason  string
	Message string
}

// revealValue resolves one key of a fetched Secret into the response, or into
// the reason there is not going to be one. It is separate from the handler
// because every rule in it is a rule about the *object* rather than about the
// request — which kinds of Secret are never revealed, what a missing key means,
// what happens to bytes that are not text — and those are the rules worth
// pinning without a cluster to ask.
func revealValue(object secretObject, namespace, name, key string) (secretValueResponse, *revealRefusal) {
	// A ServiceAccount token is not a value somebody typed into a Secret; it is
	// a live credential for this cluster's API, and handing one to a browser
	// hands over an identity rather than a fact about a workload.
	if object.Type == serviceAccountTokenType {
		return secretValueResponse{}, &revealRefusal{
			Status: http.StatusForbidden,
			Reason: "service account token",
			Message: "this is a ServiceAccount token, which is a live cluster credential " +
				"and is never revealed here",
		}
	}

	encoded, present := object.Data[key]
	if !present {
		return secretValueResponse{}, &revealRefusal{
			Status:  http.StatusNotFound,
			Reason:  "key not present",
			Message: "that secret has no such key",
		}
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return secretValueResponse{}, &revealRefusal{
			Status:  http.StatusBadGateway,
			Reason:  "value did not decode",
			Message: "the cluster returned a value that did not decode",
		}
	}
	if len(raw) > secretValueLimit {
		return secretValueResponse{}, &revealRefusal{
			Status:  http.StatusRequestEntityTooLarge,
			Reason:  "value too large to reveal",
			Message: "this value is too large to reveal in the console",
		}
	}

	out := secretValueResponse{
		Namespace: namespace,
		Name:      name,
		Key:       key,
		Type:      object.Type,
		Bytes:     len(raw),
	}
	if utf8.Valid(raw) {
		out.Value = string(raw)
	} else {
		// A TLS key's DER rendered as replacement characters is a worse answer
		// than saying it is binary: the operator asking "is this the right
		// value" needs to be able to tell a mangled reveal from a wrong secret.
		out.Binary = true
		out.Encoded = encoded
	}
	return out, nil
}

// recordSecretReveal writes this server's own record of a reveal. The Secret
// and the key are both named, because "somebody read a value out of
// `db-credentials`" is not an answer when the Secret holds four of them. They
// travel in the recorded path rather than in fields of their own: bastion.Event
// has no name today (see the Maintenance item about a `create` record that
// cannot name what it created), and the request URI names both truthfully
// without inventing a path that was never called. The value never appears —
// here, in a log, or anywhere else.
func (s *server) recordSecretReveal(
	c *gin.Context,
	user *db.User,
	cluster *db.Cluster,
	namespace string,
	status int,
	failure string,
	started time.Time,
) {
	if s.auditor == nil || user == nil || cluster == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      verbSecretReveal,
		Method:    c.Request.Method,
		Path:      c.Request.URL.RequestURI(),
		Namespace: namespace,
		Resource:  "secrets",
		Status:    status,
		Duration:  time.Since(started),
		Error:     failure,
	})
}
