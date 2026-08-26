# Give someone access

This walks through the fastest path from "an empty user list" to "a developer has a kubeconfig for one cluster." It uses the console; every step has an equivalent API call under [The access model](../access/model.md) and [Users and groups](../access/users-and-groups.md).

## 1. Create the account — or let SSO create it

=== "Local account"

    In **Admin → Users**, create a user with a username and password. Leave `system_role` as `user` unless this person needs to administer kubemg itself — that is a separate decision from what clusters they can reach.

    ```
    POST /api/v1/users
    { "username": "ada", "email": "ada@example.com", "password": "...", "system_role": "user" }
    ```

=== "Federated account"

    If a [single sign-on provider](../access/sso.md) is configured with `allow_jit` on, nothing needs creating here at all: the account is provisioned the first time the person signs in through that provider, and its group memberships and cluster grants can be handled entirely by [group mappings](../access/sso.md#group-mappings). This section covers the manual path for when there is no mapping rule for a given case.

A new account has `is_active: true` and no cluster grants at all. It exists, but it can reach nothing.

## 2. Put them in a group

Groups are the reusable unit — grant a group a role once and every member inherits it. In **Admin → Groups**, create a group (or use an existing one) and add the user as a member:

```
POST /api/v1/groups                    { "name": "platform-devs" }
POST /api/v1/groups/:id/members        { "user_id": 42 }
```

A group with no cluster grants of its own does nothing yet — membership alone confers nothing.

## 3. Grant a role on a cluster

In **Admin → Permissions** (the permission matrix), grant the group — or the individual user, if this access is not meant to be shared — a Kubernetes role on one cluster:

```
POST /api/v1/permissions/assign
{
  "subject_type": "group",
  "subject_id": 7,
  "cluster_id": 3,
  "k8s_role": "edit",
  "namespaces": ["team-a"]
}
```

`k8s_role` is one of `view`, `edit`, `cluster-admin`. `namespaces` is optional: omit it (or send an empty list) for cluster-wide access, or name one or more namespaces to scope the grant. See [The access model](../access/model.md#where-namespace-scope-is-enforced) for exactly what a namespace scope does and does not restrict.

If the user already belongs to several groups, or holds a direct grant as well as an inherited one, kubemg resolves all of them into one **effective** grant per cluster — the more permissive role wins, and namespace scopes union. See [Effective access](../access/model.md#effective-access).

If this is access needed for a few hours rather than standing access, use [just-in-time elevation](../access/jit.md) instead of a permanent grant.

### Scoping to a namespace instead of the whole cluster

The example above already scoped the grant to `team-a`. To do it deliberately: send `namespaces` as a non-empty list, one entry per namespace the group or user should reach —

```json
POST /api/v1/permissions/assign
{ "subject_type": "group", "subject_id": 7, "cluster_id": 3, "k8s_role": "edit", "namespaces": ["team-a", "team-a-staging"] }
```

From here on, every namespaced read and write this group makes on cluster 3 is answered from those two namespaces specifically — never by listing the whole cluster and filtering, which would let a scoped member enumerate namespaces they were never given (see [Where namespace scope is enforced](../access/model.md#where-namespace-scope-is-enforced)). A cluster-scoped list (Nodes, PersistentVolumes, CustomResourceDefinitions) is unaffected by the scope and reads cluster-wide regardless, because there is no namespace on those kinds to check. Trying a cluster-wide list against a scoped grant — `all_namespaces=true`, or a cluster-scoped route like `clusterroles` — is refused outright rather than silently narrowed.

## 4. Verify the grant with a SubjectAccessReview

Before handing over a kubeconfig, ask the cluster itself whether the grant means what it is supposed to — rather than trusting kubemg's own bookkeeping. `POST /api/v1/clusters/:id/resources/access-review` runs a live `SubjectAccessReview` against the target cluster, under the *caller's own* impersonated identity, and returns the authorizer's real verdict:

```json title="POST /api/v1/clusters/3/resources/access-review"
{
  "subject": "ada",
  "groups": ["kubemg:edit", "kubemg:users"],
  "verb": "delete",
  "resource": "pods",
  "namespace": "team-a"
}
```

```json title="200 OK"
{ "allowed": true, "reason": "allowed by ClusterRoleBinding \"kubemg-edit\"", "subject": "ada", "verb": "delete", "resource": "pods", "namespace": "team-a" }
```

`GET /api/v1/clusters/3/resources/access-review/identity` fills in the `subject`/`groups` fields for you — it returns the exact `Impersonate-User` and `Impersonate-Group` values the proxy would put on the wire for the caller's own grant on that cluster (`subject`, `groups`, `k8s_role`, `namespaces`), read from the same function the proxy itself uses, so the review is never asked about an identity that does not match what actually gets impersonated. `GET /api/v1/clusters/3/resources/access-review/verbs` lists the fixed set of RBAC verbs the form may offer. A namespaced question is checked against the caller's own grant exactly like a normal namespaced read — asking about a namespace outside it is refused, and asking a cluster-wide question from a namespace-scoped grant is refused the same way an all-namespaces list is. See [Cluster RBAC visibility](../access/rbac-visibility.md) for the read-only Roles/Bindings inventory this pairs with.

## 5. What they see on login

On sign-in, the fleet Overview shows only the clusters this account can act on — `ClustersForUser` filters to direct grants and group-inherited ones, live at the moment of the read. A cluster the account cannot reach simply is not in the list; there is no partial view of it.

Opening a cluster from Explore reaches exactly the namespaces and objects the effective grant covers. A `view` role can read but not write or delete; `edit` can also write and delete workloads; `cluster-admin` can do everything kubemg's own RBAC bindings allow, cluster-wide. This is enforced by the target cluster's own RBAC via impersonation, not by kubemg guessing — see [The access model](../access/model.md#how-a-grant-becomes-access-on-the-wire).

## 6. Getting a kubeconfig

Once a grant exists, the user (or an admin on their behalf) can generate a kubeconfig from the cluster's page — **Generate kubeconfig** opens a sheet, no separate route needed. The file differs by connection mode:

- **Agent mode**: the file points at kubemg's own proxy and carries a kubemg-issued, proxy-scoped JWT confined to that one cluster's route.
- **Direct mode**: the file carries a token minted directly on the cluster via the Kubernetes TokenRequest API.

See [Kubeconfigs](../access/kubeconfigs.md) for the TTL ladder, the two ceilings, and how revocation differs between the two modes.

For a pipeline or automated caller rather than a person, use a [machine account](../access/machine-accounts.md) instead — it is issued a long-lived, revocable token rather than a kubeconfig tied to someone's session.

## 7. Confirm the audit trail sees it

Every one of the calls above — creating the account, adding the group membership, assigning the permission, running the access review, generating the kubeconfig, and then whatever Ada actually does with `kubectl` once the file is in her hands — is a row somewhere in the trail:

- The IAM writes (user, group, membership, permission) are administrative actions, visible in the console's own history for those pages.
- The access review and every proxied call Ada makes through the kubeconfig go through `GET /api/v1/audit`, filterable by `user_id=42` (or `cluster_id=3`) to see just this rollout. As an ordinary account, Ada herself sees only her own rows if she looks — the trail narrows a non-admin caller to their own activity regardless of what `user_id` is passed, the same rule [just-in-time access](../access/jit.md#statuses-and-transitions) requests follow.
- If Ada opens a shell (`kubectl exec`), that session shows up twice in the trail — opened, then closed with duration and byte counts — and, if session recording is enabled for the fleet, as a replayable row in [Session recording](../audit/session-recording.md) as well.

Seeing Ada's `edit` calls land as `edit`-shaped verbs against `team-a` (and refused everywhere else) is the actual confirmation that the whole chain — group membership, permission grant, namespace scope, impersonation, the cluster's own RBAC — did what steps 2–4 set out to check.
