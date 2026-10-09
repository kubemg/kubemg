# The access model

How kubemg decides who someone is and what they may do on a cluster. Read this to understand why a grant resolves the way it does; the day-to-day screens are in [Users and groups](users-and-groups.md).

## System roles

| Role | Meaning |
| --- | --- |
| `superadmin` | Full control, including managing other super admins. The account an IdP outage or an administrative mistake cannot lock you out of. |
| `admin` | Administers kubemg: users, groups, permissions, clusters, settings, guardrails. |
| `user` | An ordinary account. What it reaches on a cluster is entirely a function of its grants. |

Pages and routes that only need "administrator or not" use a coarser `admin`/`user` role derived from the system role, so the two never disagree. A super admin counts as an administrator everywhere. A [machine account](machine-accounts.md) is always `user`.

## Cluster grants

A grant ties a user (directly) or a group (inherited by its members) to a cluster:

| Field | Meaning |
| --- | --- |
| `k8s_role` | `view`, `edit` or `cluster-admin`. |
| `namespaces` | Empty means cluster-wide; otherwise the grant is limited to the listed namespaces. |
| `source` | `local` (an administrator wrote it), `sso` (a federation mapping derived it, see [Single sign-on](sso.md)) or `jit` (a time-bound elevation, see [Just-in-time access](jit.md)). |
| `expires_at` | Empty for a standing grant; set for a JIT elevation. |

A user can hold several grants for one cluster at once. They are merged, never overwritten.

## Effective access

One resolution answers "what can this person do right now on every cluster": direct grants merged with everything inherited from groups. Adding someone to a group can never take access away.

1. **An expired grant is dropped on read.** A JIT elevation stops counting the second its window ends, not when a background sweep deletes the row.
2. **Several grants for one cluster are merged.** The stronger role wins (`cluster-admin` > `edit` > `view`). If either side is cluster-wide, the result is cluster-wide. Otherwise the namespace lists are unioned. The merged result has no expiry if either side has none.

### Worked example

Ada has three grants on `prod-eu`:

| Source | `k8s_role` | `namespaces` | `expires_at` |
| --- | --- | --- | --- |
| Direct (`local`) | `view` | cluster-wide | none |
| Group `platform-devs` (`local`) | `edit` | `team-a,team-b` | none |
| JIT elevation (`jit`) | `cluster-admin` | cluster-wide | in 40 minutes |

While the elevation is live, Ada's effective access is cluster-wide `cluster-admin` with no countdown, because her permanent `view` grant is merged underneath it. When the window ends, the JIT grant is dropped and she is back to `edit` on `team-a,team-b`, the merge of her other two grants. No restore step, no gap.

## How a grant becomes access on the wire

kubemg holds no per-user credentials on target clusters. Every proxied call is impersonated: the bastion sets `Impersonate-User` to `kubemg:u:<username>` and `Impersonate-Group` to the role's group plus `kubemg:users`. Any credential or impersonation header the client sent is stripped first, so a caller cannot widen what it is impersonated as.

```
Impersonate-User: kubemg:u:ada
Impersonate-Group: kubemg:edit
Impersonate-Group: kubemg:users
```

| Effective `k8s_role` | `Impersonate-Group` |
| --- | --- |
| `view` (also used when the role is empty) | `kubemg:view`, `kubemg:users` |
| `edit` | `kubemg:edit`, `kubemg:users` |
| `cluster-admin` | `kubemg:cluster-admin`, `kubemg:users` |

Groups carry the role, never the namespace list; namespace scope is enforced by the proxy (below).

### Why the username is prefixed

Without a prefix, an account named `system:serviceaccount:kube-system:backup-operator` would reach the API server as that ServiceAccount and inherit its bindings, whatever its kubemg grant said. Three things close this together:

- **The prefix.** A kubemg account is always `kubemg:u:<username>` to the cluster.
- **The username rule.** A new or renamed account may not contain `:` or a control character. A federated sign-in whose username claim breaks the rule is refused by name, not rewritten. Older accounts are left alone and listed in a warning at startup so you can rename them.
- **The agent's permissions.** The agent may impersonate only the four `kubemg:` groups and no ServiceAccount.

If you bound a RoleBinding to a kubemg username directly (`kind: User, name: ada`), rebind it to `kubemg:u:ada`. Bindings to the `kubemg:` groups, which is how kubemg's own manifests grant access, are unaffected. kubemg's own fixed identities (`kubemg:alarm-watcher`, `kubemg:event-watcher`, `kubemg:shell-runner`) keep their names.

## Where namespace scope is enforced

Impersonation cannot express "only these namespaces", so the proxy enforces it: a scoped grant refuses any call naming a namespace outside its list. Discovery paths and cluster-scoped kinds are exempt (there is no namespace to check). A resource list for a scoped grant is answered by reading each granted namespace and merging the results, never by listing the whole cluster and filtering, so a scoped user cannot learn which other namespaces exist.

## What each role can do

The role's meaning inside an agent-mode cluster comes from ClusterRoles and bindings in the agent's install manifests. The cluster's own RBAC decides every call; kubemg does not keep a second copy of "can `view` write".

| Group | Bound to | Grants |
| --- | --- | --- |
| `kubemg:view` | built-in `view` | Read-only on most namespaced resources. No write verbs. |
| `kubemg:edit` | built-in `edit` | `view` plus create/update/delete on workloads and other namespaced resources. No cluster-scoped objects such as Nodes or ClusterRoles. |
| `kubemg:cluster-admin` | built-in `cluster-admin` | Full control cluster-wide, including RBAC. |
| `kubemg:users` | CRD discovery, `kubemg-custom-resource-view`/`-edit`, `system:discovery` | Baseline on every call: list which CRDs exist, read (and, at `edit` or above, write) Gateway API and five Istio groups, and API discovery. |

Browsing a custom resource from an operator outside those groups is a generic list and YAML editor with no RBAC to read or write it, unless an administrator adds that API group to `kubemg-custom-resource-view`/`-edit` and re-applies the manifests.

## The direct-mode gap

In direct mode kubemg mints a token but binds no RBAC to it, so the permission matrix governs only kubemg's own authorization, not the cluster's. Agent mode closes the gap. Details in [Connection modes](../clusters/connection-modes.md). This is why [machine accounts](machine-accounts.md) refuse direct mode.

## Disabled accounts

Disabling an account takes effect immediately, not when its session token expires. Every request re-reads the account and a disabled one gets `403 this account is disabled`. A machine token stops at its next call too.

## Self-protection rules

- A caller can never delete, disable or change the system role of **its own** account. This is what guarantees an active admin always remains, even for the only admin left.
- Only a super admin may create or manage another super admin.

## FAQ

**What happens to an open shell, followed log or port-forward when a grant changes?**

- **Agent mode:** the change applies to anything opened after it, at the very next call. A socket already open is not interrupted.
- **Direct mode:** the kubeconfig's token is valid on the cluster until it expires, whatever happens to the grant in kubemg.

??? info "Why a scoped user is not answered by filtering a cluster-wide list"
    Listing everything and discarding rows outside the grant would still leak the names of the other namespaces. Reading the granted namespaces one at a time never issues a cluster-wide list.
