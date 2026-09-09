# Cluster RBAC visibility

kubemg's own [permission matrix](model.md) governs *kubemg's* authorization —
who may open which cluster, in which namespaces, with which role. What that
grant is actually **worth** is decided somewhere else entirely: by the
target cluster, through impersonation, against RBAC bindings kubemg did not
write and does not own. The routes on this page are what let an operator
check that instead of taking it on faith.

## Reading a cluster's own RBAC

Five read-only routes, all under `/api/v1/clusters/:id/resources/`, list
Roles, ClusterRoles, RoleBindings, ClusterRoleBindings and ServiceAccounts as
ordinary Explore resources, down the same impersonated tunnel as any other
list:

| Route | Scope |
|---|---|
| `GET .../roles` | namespaced, scoped like any namespaced list |
| `GET .../clusterroles` | cluster-wide; refused to a namespace-scoped grant |
| `GET .../rolebindings` | namespaced |
| `GET .../clusterrolebindings` | cluster-wide; refused to a namespace-scoped grant |
| `GET .../serviceaccounts` | namespaced |

A **Role or ClusterRole** row carries a bounded prefix of its policy rules
(up to `maxRuleSummary`, 12) plus the full verb and resource sets unioned
across every rule, so a list of forty roles is scannable in one line each.
Two properties are surfaced explicitly rather than left for a reader to spot:
**`aggregated`** marks a ClusterRole the controller assembles from other
ClusterRoles' labels — its rules are an output, so editing them directly
achieves nothing — and **`wildcard`** marks a role with a `*` verb or a `*`
resource, which is the property that turns an apparently narrow role into a
broad one. `builtin` marks a role stamped with Kubernetes' own
`kubernetes.io/bootstrapping` label — most of a fresh cluster's ClusterRole
list.

A **RoleBinding or ClusterRoleBinding** row resolves the question the way it
is actually asked — *who* gets *what* — rather than printing a bare
`roleRef` an operator would then have to look up by hand: `role_kind`/`role_name`
name the bound Role or ClusterRole, `cluster_scoped` marks a
ClusterRoleBinding (whose grant reaches every namespace at once — the field
that turns a binding list into a statement about blast radius), and
`subjects` carries up to `maxSubjects` (20) resolved subjects with `kinds`
naming the set of subject kinds present, so a binding onto two hundred
subjects reads as a count rather than a wall of names.

A **ServiceAccount** row is treated as an identity, not a workload: it
carries the `secrets`/`image_pull_secrets` counts, its own
`automount_token` setting (`nil` when unset, meaning the pod spec decides
— never conflated with an explicit `false`), and whether it is the
`default` every namespace gets whether anyone asked for one.

### kubemg reads RBAC; it never authors it

Every route above is a `GET`. There is no write route in this family at all
(the access review below is the one exception, and it is a review, not an
authoring act — see below). This is a deliberate boundary, not a missing
feature: writing a cluster's RBAC from a tool whose own permission model is
entirely separate is exactly how the two would silently diverge.

That boundary is enforced in the generic manifest editor too, not just by
the absence of a dedicated write route. Creating a `roles`, `rolebindings`,
`clusterroles` or `clusterrolebindings` object from a manifest is refused, with
the response naming the reason —

> kubemg does not author a cluster's RBAC. Create Roles and bindings with
> kubectl or whatever manages them, and read them back here.

This refusal covers **creation only**. The generic editor's `PUT` (updating
an *existing* Role or binding object) is not specially blocked for these four
kinds — an operator with edit access can still hand-edit a Role that already
exists, the same as any other object's manifest. What kubemg will never do on
its own initiative is author a *new* RBAC object, which is the half of "does
not author RBAC" that actually matters: RBAC objects are the thing that
decides who may do what, and nothing here manufactures one from a template.
`serviceaccounts` and `nodes` are the two other kinds with their own entries
in the same deny list — a Node is not created, it joins when its kubelet
registers, and posting one manufactures a cluster member no kubelet is
behind.

## Access review: what the authorizer will actually do

The inventory above shows what is **written down**. It does not show what the
authorizer will **do** with it, and the gap between the two is exactly where
an audit finding lives: aggregation assembles rules out of labels, a wildcard
means more than it looks like, one subject can be reached through three
bindings at once, and the cluster might be running an authorizer that is not
RBAC at all (a webhook, node, or cloud-provider authorizer). Deriving a
verdict by walking bindings by hand would be kubemg guessing at an answer the
cluster is willing to state outright.

`POST /api/v1/clusters/:id/resources/access-review` asks instead. It sends a
`SubjectAccessReview` — **not** a `SelfSubjectAccessReview`, deliberately: it
is asked about a *named* subject (a user, a group, or
`system:serviceaccount:<namespace>:<name>`), so it can answer "may this
*other* identity do this" rather than only "may I." The request names the
subject and optional groups, plus the verb/group/resource/subresource/name/namespace
being asked about:

```json
{
  "subject": "jane@example.com",
  "groups": ["kubemg:view"],
  "verb": "delete",
  "resource": "pods",
  "subresource": "exec",
  "namespace": "payments"
}
```

The result is quoted, not interpreted:

- `allowed` — the authorizer's plain verdict.
- `denied` — a *different* thing from `allowed: false`: an authorizer can
  **explicitly** deny, and no later authorizer in the chain can then permit
  it. Collapsing this into "not allowed" sends someone off to write a
  RoleBinding that cannot help.
- `evaluation_error` — the authorizer could not finish; a review that errored
  is not a denial, and reporting it as one would misstate the cluster.
- `reason` — the authorizer's own explanation, usually naming the binding
  that decided it.

Two properties shape how this is gated: it is a `create` against
`authorization.k8s.io` in RBAC's own eyes, even though it changes nothing on
the cluster — so **a caller whose grant does not carry `create` on
`subjectaccessreviews` is refused**, and that refusal is surfaced as the
cluster's own answer about the caller rather than hidden behind an empty
result (asking what an arbitrary identity may do is itself privileged
information). And the review runs under the **caller's own** impersonated
identity, asking about someone else — which is why it cannot be used to
escalate: an operator who cannot create the review learns nothing, and one
who can was already trusted with the answer.

A namespaced question is checked against the caller's own grant the same way
any other namespaced read is (a scoped caller cannot use a review to probe a
namespace outside their grant), and a cluster-wide question from a
namespace-scoped grant is refused outright for the same reason a cluster-wide
*list* would be.

`GET .../resources/access-review/verbs` serves the fixed verb vocabulary the
review form may offer (`get`, `list`, `watch`, `create`, `update`, `patch`,
`delete`, `deletecollection`, `impersonate`, `bind`, `escalate`, `use`, `*`)
— served rather than compiled into the frontend, on the same principle as the
JIT window presets: the only honest source for what will be accepted is what
the server will accept.

### The identity endpoint

`GET .../resources/access-review/identity` answers a narrower, more personal
question: **what does my own grant actually amount to on this cluster?** It
reports the exact `subject` and `groups` kubemg would impersonate for the
caller — the same values the proxy puts on the wire as `Impersonate-User`/
`Impersonate-Group` — plus the resolved `k8s_role` and `namespaces`. Asking a
review about that identity is how the permission matrix's promise ("you have
`view` on `payments`") gets checked against what the cluster's own RBAC
actually grants that impersonated identity, rather than trusted on kubemg's
word alone.

## How this differs from the kubemg permission matrix

The [permission matrix](model.md) and [users and groups](users-and-groups.md)
pages answer a different question: who may open **kubemg**, at what role,
scoped to which namespaces. That grant decides what identity gets
impersonated and what a scoped caller's namespace list is — it is the input
to every read on this page. It says nothing about what the *target cluster's*
own RBAC does with that impersonated identity once a call reaches it, which
is exactly the gap this page's inventory and access review close: two
separate authorization layers, read here side by side rather than conflated
into one.

## See also

- [Access model](model.md) — kubemg's own grant model this page's reads sit
  downstream of.
- [Users and groups](users-and-groups.md) — where a grant is actually
  assigned.
- [Security posture](../clusters/security-posture.md) — the automounted
  default ServiceAccount rule, which reads the same ServiceAccount objects
  this page lists.
