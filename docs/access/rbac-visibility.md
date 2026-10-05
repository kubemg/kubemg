# Cluster RBAC visibility

Read a cluster's own Roles, bindings and ServiceAccounts from the console, and ask the cluster what any identity may do. kubemg's [permission matrix](model.md) decides who may open a cluster; what that grant is worth is decided by the cluster's RBAC, which these read-only routes let you check.

## Reading a cluster's own RBAC

Five routes under `/api/v1/clusters/:id/resources/` list RBAC objects as ordinary Explore resources, down the same impersonated tunnel as any list:

| Route | Scope |
|---|---|
| `GET .../roles` | namespaced |
| `GET .../clusterroles` | cluster-wide; refused to a namespace-scoped grant |
| `GET .../rolebindings` | namespaced |
| `GET .../clusterrolebindings` | cluster-wide; refused to a namespace-scoped grant |
| `GET .../serviceaccounts` | namespaced |

What each row shows:

- **Role / ClusterRole:** the first 12 rules plus the union of all verbs and resources. Flags: `aggregated` (assembled from other ClusterRoles' labels, so editing its rules achieves nothing), `wildcard` (a `*` verb or resource, which makes a narrow-looking role broad) and `builtin` (Kubernetes' own bootstrapping label).
- **RoleBinding / ClusterRoleBinding:** who gets what. `role_kind`/`role_name` name the bound role, `cluster_scoped` marks a ClusterRoleBinding (reaches every namespace), and `subjects` lists up to 20 with the subject `kinds` present.
- **ServiceAccount:** an identity, with `secrets`/`image_pull_secrets` counts, its `automount_token` setting (unset is kept distinct from an explicit `false`) and whether it is the namespace `default`.

### kubemg reads RBAC; it never authors it

Every route above is a `GET`. Creating a `roles`, `rolebindings`, `clusterroles` or `clusterrolebindings` object from a manifest is refused:

> kubemg does not author a cluster's RBAC. Create Roles and bindings with kubectl or whatever manages them, and read them back here.

This covers creation only; editing an existing Role you have edit access to is not specially blocked. `serviceaccounts` and `nodes` are also on the no-create list (a Node joins when its kubelet registers).

## Access review: what the authorizer will actually do

The inventory shows what is written down; the cluster states what it will **do**, covering aggregation, wildcards, several bindings reaching one subject, and non-RBAC authorizers. `POST /api/v1/clusters/:id/resources/access-review` asks the cluster directly with a `SubjectAccessReview` about a **named** subject (a user, a group, or `system:serviceaccount:<namespace>:<name>`), so it can answer for another identity.

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

| Field | Meaning |
| --- | --- |
| `allowed` | The authorizer's verdict. |
| `denied` | An **explicit** deny that no later authorizer can override. Different from `allowed: false`, which a new RoleBinding could fix. |
| `evaluation_error` | The authorizer could not finish. Not a denial. |
| `reason` | The authorizer's own explanation, usually naming the deciding binding. |

Gating:

- The review runs under **your own** impersonated identity, and the cluster treats it as a `create` on `subjectaccessreviews`. If your grant lacks that, you are refused with the cluster's own answer, so a review cannot be used to escalate.
- A namespaced question is checked against your grant like any namespaced read; a cluster-wide question from a namespace-scoped grant is refused.

`GET .../resources/access-review/verbs` serves the verbs the form offers (`get`, `list`, `watch`, `create`, `update`, `patch`, `delete`, `deletecollection`, `impersonate`, `bind`, `escalate`, `use`, `*`).

### The identity endpoint

`GET .../resources/access-review/identity` answers "what does my own grant amount to here?" It returns the exact `subject` and `groups` kubemg impersonates for you, plus your `k8s_role` and `namespaces`. Reviewing that identity checks the matrix's promise ("you have `view` on `payments`") against what the cluster's RBAC actually grants.

## How this differs from the kubemg permission matrix

The [permission matrix](users-and-groups.md#the-permission-matrix) says who may open **kubemg**, at what role and namespaces, which determines the impersonated identity. This page covers what the **cluster's** RBAC does with that identity. They are two authorization layers, read side by side.

## See also

- [Access model](model.md)
- [Users and groups](users-and-groups.md)
- [Security posture](../clusters/security-posture.md)
