# The access model

Every read and write kubemg makes on a target cluster passes through the same question twice: who is this, and what may they do here. This page is the mechanics behind both answers.

## System roles

Every account (`db.User`) carries a `SystemRole`, one of:

| Role | Meaning |
| --- | --- |
| `superadmin` | Full administrative control, including managing other super admins. The tier that exists to be the account an IdP outage — or an administrative mistake — cannot lock the operator out of. |
| `admin` | Administers kubemg itself: users, groups, permissions, clusters, settings, guardrails. |
| `user` | An ordinary account. What it can reach on any given cluster is entirely a function of its grants. |

`Role` is the coarse value carried in the JWT and checked by `RequireRole` middleware — it is **derived from `SystemRole`**, never written independently:

```go
func LegacyRoleFor(systemRole string) string {
    switch systemRole {
    case SystemRoleSuperAdmin, SystemRoleAdmin:
        return RoleAdmin
    default:
        return RoleUser
    }
}
```

`User.Normalize()` is what fills `Role` in from `SystemRole` on every read and write. Nothing in the codebase sets `Role` by hand; a super admin is an "administrator" (`user.IsAdmin()`) everywhere the coarse role is checked, including which body `ClusterSummary` renders and whether `Run check` is offered.

A machine account (`AccountType: "machine"`) is pinned to `SystemRoleUser` by `Normalize` — a row edited directly in the database cannot smuggle admin onto a credential that lives in a CI secret store. See [Machine accounts](machine-accounts.md).

## Cluster grants

Access to a specific cluster is a row in `user_cluster_access` (direct) or `group_cluster_access` (inherited via group membership), each carrying:

- **`k8s_role`** — one of `view`, `edit`, `cluster-admin`.
- **`namespaces`** — optional. Empty means cluster-wide; a comma-joined list scopes the grant to those namespaces.
- **`source`** — `local` (an administrator wrote it by hand), `sso` (a federation mapping derived it — see [Single sign-on](sso.md)), or `jit` (a time-bound elevation — see [Just-in-time access](jit.md)).
- **`expires_at`** — nil for a standing grant, set for a JIT elevation.

A user can hold several rows for the same cluster at once — a standing grant, a federated one, a live elevation — and they are merged rather than one overwriting another.

## Effective access

`Store.AccessForUser` is the one function that answers "what can this person do, right now, on every cluster" — direct grants merged with everything inherited from the caller's groups:

```go
// AccessForUser returns the user's effective cluster grants keyed by cluster
// ID: direct grants merged with everything inherited from their groups. The
// more permissive grant wins, so adding someone to a group can never take
// access away.
```

Two things happen inside it that matter everywhere downstream:

1. **An expired grant is dropped on read.** The query filters on `expires_at IS NULL OR expires_at > now()`. A JIT elevation stops counting the second its window ends — not when a background sweeper gets around to deleting the row — because this is the read every proxied call, kubeconfig generation, and permission check goes through.
2. **Multiple rows for one cluster are merged, not overwritten**, by `MergeAccess`:
    - The stronger role wins (`cluster-admin` > `edit` > `view`).
    - If either side is unscoped (`namespaces == ""`), the merged result is unscoped — a standing view grant plus a bounded cluster-admin elevation is access that does not end when the elevation does.
    - Otherwise the namespace lists union.

This is why a user added to a group never loses access they already held directly, and why a temporary elevation never leaves a gap once it expires — the standing row is still there, merged, underneath it.

### Worked example

Ada has three rows that all resolve against the same cluster, `prod-eu`:

| Source | Row | `k8s_role` | `namespaces` | `expires_at` |
| --- | --- | --- | --- | --- |
| Direct grant | `local` | `view` | `` (cluster-wide) | nil |
| Group grant (`platform-devs`) | `local` | `edit` | `team-a,team-b` | nil |
| JIT elevation, approved 20 minutes ago | `jit` | `cluster-admin` | `` (cluster-wide) | in 40 minutes |

`AccessForUser` folds these three left to right through `MergeAccess`:

1. **Direct `view` (cluster-wide) + group `edit` (`team-a,team-b`)** — the stronger role wins (`edit` beats `view`), and because the direct row is unscoped (`namespaces == ""`), the merge rule "either side unscoped ⇒ result unscoped" makes the intermediate result `edit`, cluster-wide.
2. **That result + the JIT `cluster-admin` row** — `cluster-admin` beats `edit`, and the JIT row is itself cluster-wide, so the merged role is `cluster-admin`, cluster-wide. On expiry, `MergeAccess` takes `nil` whenever *either* side's `expires_at` is nil, and otherwise takes the **later** of two non-nil expiries. Both standing rows here carry `expires_at = nil`, so the final merged row is unscoped `cluster-admin` with `expires_at = nil` — a permanent grant merged with a temporary one reads as permanent, exactly as the model.md summary above states ("a standing view grant plus a bounded cluster-admin elevation is access that does not end when the elevation does").

**Effective result while the elevation is live:** unscoped `cluster-admin`, with no countdown attached to the effective row, because the permanent `view` row is still there merging in underneath it.

**The instant the JIT row's window passes:** `AccessForUser`'s `expires_at IS NULL OR expires_at > now()` filter drops that row from the query entirely — it is never handed to `MergeAccess` at all — so the effective grant is recomputed from the direct and group rows alone. Ada is back to `edit` scoped to `team-a,team-b`, exactly what she held before the elevation, with no restore step and no gap.

## How a grant becomes access on the wire

kubemg never manages per-user credentials on target clusters. Every proxied call is impersonated: the bastion sets `Impersonate-User` to the caller's own username and `Impersonate-Group` to a pair of groups derived from the resolved role:

```go
func ImpersonationGroups(k8sRole string) []string {
    if k8sRole == "" {
        k8sRole = db.K8sRoleView
    }
    return []string{GroupPrefix + k8sRole, GroupAllUsers}
}
```

That is `kubemg:view` / `kubemg:edit` / `kubemg:cluster-admin`, plus `kubemg:users` on every call regardless of role, giving the cluster one subject to hang baseline access off. Client-supplied credentials and impersonation headers on the incoming request are stripped before kubemg's own are set — nothing a caller sends can widen what it is impersonated as.

For a caller named `ada` with effective role `edit`, the header set the agent's Kubernetes API server actually sees is:

```
Impersonate-User: ada
Impersonate-Group: kubemg:edit
Impersonate-Group: kubemg:users
```

Every resolved role produces exactly this shape — `Impersonate-User` is always the caller's own username (never a shared service identity), and `Impersonate-Group` always carries two values: the one group for the resolved role, and `kubemg:users` unconditionally:

| Effective `k8s_role` | `Impersonate-Group` values |
| --- | --- |
| `view` | `kubemg:view`, `kubemg:users` |
| `edit` | `kubemg:edit`, `kubemg:users` |
| `cluster-admin` | `kubemg:cluster-admin`, `kubemg:users` |
| *(empty/unset)* | `kubemg:view`, `kubemg:users` — `ImpersonationGroups` treats an empty role as `view` |

Nothing here is scoped by namespace: impersonation groups carry the *role*, never the namespace list — that half of the grant is enforced in the proxy itself, described below.

## Where namespace scope is enforced

The namespace scope on a grant is a kubemg concept that Kubernetes impersonation groups cannot express — there is no `Impersonate-Group` that means "only these three namespaces." So it is enforced by the **proxy itself**: a scoped grant refuses any call naming a namespace outside its list (except discovery paths, and cluster-scoped kinds, which are read cluster-wide because there is no namespace to check). A resource list for a scoped grant is answered by reading the grant's own namespaces one at a time and merging results — never by listing the whole cluster and filtering, which would let a scoped user enumerate namespaces they were never given.

## Why the role itself is deliberately *not* enforced locally

kubemg resolves *which* role applies and sets the impersonation header accordingly — but whether `view` may only read, or `edit` may also delete, is answered by the **target cluster's own RBAC**, through the `kubemg:view` / `kubemg:edit` / `kubemg:cluster-admin` ClusterRoleBindings the agent manifests install. kubemg does not duplicate that decision locally. This is deliberate: the cluster's RBAC is the one place that already has to get this right, and a second, kubemg-side copy of "can `view` write" would only be a second place for the two to disagree.

### What each role can actually do, on the wire

The bindings in `deploy/kustomize/base/rbac.yaml` (and its embedded copy under `backend/pkg/agentpkg/base/`, which `make manifest-check` keeps in lockstep) are what give the three roles their meaning inside an agent-mode cluster:

| Group | Bound to | What it grants |
| --- | --- | --- |
| `kubemg:view` | the built-in `view` ClusterRole | Read-only access to almost everything in the built-in and aggregated-to-view API groups — the same role `kubectl auth can-i --as` would show for a Kubernetes "viewer". Explicitly excludes Secrets' contents (the built-in `view` role can list Secret *objects* but not read most other sensitive resources) and any write verb. |
| `kubemg:edit` | the built-in `edit` ClusterRole | Everything `view` gets, plus create/update/patch/delete on the workload- and namespace-scoped resources `edit` covers — Deployments, Services, ConfigMaps, and so on — but not cluster-scoped objects like Nodes, ClusterRoles, or other namespaces' RBAC. |
| `kubemg:cluster-admin` | the built-in `cluster-admin` ClusterRole | Full control, cluster-wide, including RBAC itself. This is the role a JIT elevation to `cluster-admin` actually grants inside the cluster, not just in kubemg's own database. |
| `kubemg:users` | `kubemg-crd-discovery` (read `customresourcedefinitions`), `kubemg-custom-resource-view`/`-edit` (the Gateway API and five Istio groups, enumerated, never wildcarded), `system:discovery` | Baseline access every proxied call carries regardless of role: seeing which CRDs exist (a schema, not the data it holds), reading and — if the resolved role is `edit` or above — writing objects in the Gateway API and Istio groups, and the API discovery `kubectl` needs before it can resolve a single resource. |

Two things follow directly from this table. First, a `view` grant genuinely cannot write to any of kubemg's first-class custom-resource groups either — `kubemg-custom-resource-edit` is bound to `kubemg:edit`, not `kubemg:users`, so `view` only ever picks up the read-only `kubemg-custom-resource-view` binding via `kubemg:users`. Second, browsing a CRD from an operator kubemg has not enumerated (anything outside the Gateway API and Istio groups) is a **generic list and a YAML editor with no RBAC to read or write it** unless an administrator adds that operator's API group to `kubemg-custom-resource-view`/`-edit` and re-applies the manifests — this is stated in the manifest's own comments and is the one thing standing between the generic tooling and a CRD nobody here has heard of.

## The direct-mode gap

In **direct** connection mode, this closes only partially. kubemg mints a token on the target cluster via TokenRequest, but provisions **no RoleBinding** for it — so a generated kubeconfig authenticates against the cluster without the cluster having any opinion on what that identity may do. The permission matrix in direct mode governs *kubemg's own* authorization (whether the console lets someone generate the file at all, and what it fills in), not the target cluster's RBAC.

In **agent** mode this gap closes: the installed manifests bind `kubemg:view`/`kubemg:edit`/`kubemg:cluster-admin` to real ClusterRoles, and the proxy's impersonation headers mean the cluster's own RBAC decides every call. This is why programmatic access via [machine accounts](machine-accounts.md) refuses direct mode outright — a credential this console cannot see authorized on the cluster is not one it should hand out for unattended use.

The cluster detail page, the permissions page, and the registration wizard's last step all disclose which of the two modes a given cluster is in, and this disclosure is treated as load-bearing — it must stay honest and mode-aware wherever it appears.

## Disabled accounts

Disabling an account (`is_active: false`) takes effect immediately, not when an already-issued JWT happens to expire. `currentUser` — the resolver behind every authenticated request — re-reads the account on every call and rejects it:

```go
if !user.IsActive {
    c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
    ...
}
```

The same check applies to a machine token's verifier, so disabling the machine account behind a credential stops it at the next call too.

## Self-protection rules

Two rules live in the account-management handlers rather than the store, and both are deliberate:

- **A caller can never delete, disable, or change the system role of its own account.** This is the actual guarantee that an active admin always remains — there is no separate "last admin" count to maintain, because the rule holds even for the only admin left.
- **Only a super admin may create or manage another super admin.** An ordinary admin cannot promote an account to super admin, and cannot edit, disable, or delete an existing super admin's account.

Both are enforced at the handler that would otherwise perform the action (`setUserStatus`, `deleteUser`, `loadManageableUser`), not by the store layer, and they hold regardless of the caller's own tier — a super admin cannot disable itself either.

## FAQ

**Why is a namespace-scoped user answered from their grant, rather than by listing every namespace and filtering?**

Because that would let a scoped user enumerate namespaces they were never given. If kubemg listed the whole cluster and threw away rows outside the grant, the *list of namespace names* itself would leak — a scoped grant for `team-a` would still see that `team-b`, `payments-prod`, and every other namespace exist, just not their contents. Reading a scoped grant's own namespace list one at a time and merging the results, as [Where namespace scope is enforced](#where-namespace-scope-is-enforced) describes, never issues a cluster-wide list at all, so there is nothing for the response to leak.

**What happens to an open session — a shell, a followed log, a port-forward — when the underlying grant changes mid-stream?**

It depends entirely on connection mode, and this is one of the two things the raise-the-kubeconfig-TTL disclosure and the machine-account design both hinge on:

- **In agent mode**, every call — including a long-lived stream — is impersonated through the tunnel, and impersonation is resolved from a **live** read of `AccessForUser` on the call that opens the stream. A grant change (an admin revokes access, a group membership is removed, a JIT elevation expires) takes effect for anything opened *after* the change, at the very next call. It does not reach back into a socket that is already open and bridging bytes — the tunnel itself has no mechanism to interrupt a running exec or port-forward mid-stream. What is guaranteed is that a *new* stream, or a re-authenticated one, sees the change immediately, and the standing "revocation stops the file at once" language elsewhere in the manual is about calls, not about killing sessions already in flight.
- **In direct mode**, a kubeconfig carries a token minted directly on the cluster via TokenRequest. Revoking the grant in kubemg does nothing to that token's validity on the cluster — it keeps working until it expires on its own schedule, however the grant changes in the meantime. This is the direct-mode gap described above, stated again here because it is the sharper edge of it: kubemg's grant is not the thing standing between a revoked user and the cluster in this mode.
