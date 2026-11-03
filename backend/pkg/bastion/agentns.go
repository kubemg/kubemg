package bastion

import (
	"slices"
	"strings"
	"sync/atomic"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The agent's namespace is KubeMG's, not the cluster's tenants'.
//
// It holds three things a grant below cluster-admin must never reach: the
// agent's ServiceAccount, which may impersonate any user and the
// kubemg:cluster-admin group; the agent's registration Secret, which is enough
// to stand in for the agent and receive every caller's traffic; and every
// person's shell pod, each holding that person's proxy kubeconfig. The
// built-in `edit` role the kubemg:edit group is bound to can read Secrets,
// create pods under any ServiceAccount and exec into pods in every namespace
// — that one included — and RBAC has no way to carve a namespace out of a
// ClusterRoleBinding. So the gateway does: a call into the agent's namespace is
// refused for anything but a cluster-admin grant, which can already do all of
// the above by definition.
//
// KubeMG's own service identities (unsaved, ID 0 — the shell runner, the
// watchers) are not refused: managing that namespace is their job, and none of
// them is reachable by a caller.

// agentNamespaces is the published set of namespaces the agent may be
// installed in, read lock-free on every call.
type agentNamespaces struct {
	value atomic.Pointer[[]string]
}

func (a *agentNamespaces) store(namespaces []string) {
	set := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" && !slices.Contains(set, namespace) {
			set = append(set, namespace)
		}
	}
	a.value.Store(&set)
}

func (a *agentNamespaces) contains(namespace string) bool {
	set := a.value.Load()
	return set != nil && slices.Contains(*set, namespace)
}

// SetAgentNamespaces publishes where the agent runs. The API layer calls it
// with the effective setting and the boot default together: an agent installed
// before an operator changed the setting is still in the old namespace, and
// that namespace still holds its credential.
func (p *Proxy) SetAgentNamespaces(namespaces ...string) {
	p.agentNS.store(namespaces)
}

// agentNamespaceRefusal names why a call may not reach the agent's namespace,
// or "" when it may.
func (p *Proxy) agentNamespaceRefusal(user *db.User, grant db.UserClusterAccess, parsed APIPath) string {
	if user.ID == 0 || grant.K8sRole == db.K8sRoleClusterAdmin {
		return ""
	}
	if parsed.Namespace == "" || !p.agentNS.contains(parsed.Namespace) {
		return ""
	}
	return "namespace " + parsed.Namespace + " is where KubeMG's agent runs, and only a cluster-admin grant reaches it"
}

// clusterWideSecretRefusal names why a kubectl call may not read every Secret
// in the cluster at once, or "" when it may. Below cluster-admin, such a read
// carries the agent's registration Secret in its answer, and on the kubectl
// path that answer goes to the caller verbatim. The console's own lists read
// keys only and never hand a value back, so they are not refused.
func clusterWideSecretRefusal(user *db.User, grant db.UserClusterAccess, parsed APIPath) string {
	if user.ID == 0 || grant.K8sRole == db.K8sRoleClusterAdmin {
		return ""
	}
	if parsed.Namespace != "" || parsed.Resource != "secrets" {
		return ""
	}
	return "a read of every Secret in the cluster would include KubeMG's own agent credential; " +
		"only a cluster-admin grant may make one. Read Secrets one namespace at a time"
}
