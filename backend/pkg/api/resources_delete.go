package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

/*
 * Deleting one object.
 *
 * It is the same address as the manifest editor — the sidebar's own kind key,
 * a name and a namespace — because it is the same object, and a delete that
 * could reach anything the read cannot would be a second, wider surface. The
 * path is built from the fixed kind table (or, for a CRD, from the three
 * validated components `customObjectKind` builds one from), so nothing a caller
 * sends becomes an API path.
 *
 * Nothing here is a new permission. The call goes down the tunnel impersonated
 * like every other, so a `view` grant is refused by the cluster's own RBAC in
 * the cluster's own words; the namespace is checked against the grant the way
 * every namespaced read is; it passes the command guardrails, which is where an
 * operator says "not this, not here"; and it lands in the audit trail as a
 * `delete` because that is what `VerbFor` makes of the method. What this route
 * removes is the two minutes between deciding to delete a pod and finding the
 * terminal it can be deleted from.
 *
 * There is deliberately no bulk route. A selection of eight pods is eight
 * calls, for the same reason a pooled log view is one read per pod: each one is
 * a separate act against the cluster, each earns its own audit record, and a
 * partial failure — four gone, one refused by RBAC, three still there — is a
 * real answer that a single call would have to invent a shape to report. The
 * browser makes the calls and shows the outcomes one per row.
 */

// deletePropagation is the ownership policy every delete here carries.
// Kubernetes' own default varies by kind and by client; kubectl sends
// Background for almost everything, which is what an operator deleting a
// Deployment from a list expects — the object goes now and its ReplicaSets and
// pods follow. Orphan would leave the pods running under nothing, which reads
// as a delete that did not work.
const deletePropagation = "Background"

// deleteResult is what comes back. It names the object rather than echoing it:
// there is nothing left to return, and the browser needs a sentence to put
// beside the row it just acted on.
type deleteResult struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Message   string `json:"message"`
}

// deleteResourceObject removes one object from the cluster.
func (s *server) deleteResourceObject(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}

	paths := kind.objectPaths(namespace, name)
	for i, path := range paths {
		resp, callOK := s.callResourceWith(c, user, cluster, grant, http.MethodDelete,
			path+"?propagationPolicy="+deletePropagation, nil, "could not write to the cluster")
		if !callOK {
			return
		}
		// A 404 on anything but the last candidate means this kind is served by
		// another API version — the same walk `readObject` does. On the last one
		// it means the object is not there, which is the cluster's answer and is
		// reported as such rather than as a success: an operator who asked for a
		// delete and got "already gone" learned something.
		if resp.Status == http.StatusNotFound && i < len(paths)-1 {
			continue
		}
		if resp.Status < 200 || resp.Status >= 300 {
			c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
			return
		}

		c.JSON(http.StatusOK, deleteResult{
			Kind:      c.Query("kind"),
			Name:      name,
			Namespace: namespace,
			Message:   deletedMessage(name),
		})
		return
	}
}

// deletedMessage says what happened, and says it in the tense the cluster means
// it: a delete is a request for removal, and what comes back at once is the
// object marked for it. A pod with a termination grace period, or anything with
// a finalizer on it, is still there for a while after this returns.
func deletedMessage(name string) string {
	return name + " marked for deletion"
}
