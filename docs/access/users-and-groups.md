# Users and groups

Managing accounts, group membership, and the permission matrix — the console surfaces at **Admin → Users**, **Admin → Groups**, and **Admin → Permissions**, all admin-only.

## Users

`GET|POST /api/v1/users`, `PUT|DELETE /api/v1/users/:id`, `PATCH /api/v1/users/:id/status`.

### The user record

| Field | Writable? | Notes |
| --- | --- | --- |
| `id` | no | Assigned on creation. |
| `username` | yes | Unique; a conflicting rename or create answers `409 {"error": "username already taken"}`. |
| `email` | yes | Optional — local accounts are keyed by username, not email. |
| `password` | write-only | Never read back (`PasswordHash` carries `json:"-"`); minimum 8 characters (`minPasswordLength`), enforced on both create and update. |
| `system_role` | yes, conditionally | `superadmin`, `admin`, or `user`. Setting it to `superadmin` requires the caller to already be one; a caller can never change its own. |
| `role` | no | Derived from `system_role` by `Normalize()` — see [System roles](model.md#system-roles). Never accept this from a client; there is no route that lets you. |
| `is_active` | yes, via `PATCH .../status` only | Not settable through `PUT`; see below. |
| `can_view_recordings` | yes, conditionally | Grantable only by a super admin — see [The recording-viewing capability](#the-recording-viewing-capability). |
| `account_type` | no | `user` or `machine`, derived from how the row was created. The `/users` routes never produce or accept a `machine` row. |
| `auth_source` | no | `local` or a federation provider identifier — see [Federated vs. local accounts](#federated-vs-local-accounts). |
| `last_login_at` | no | Stamped by `recordLogin` on a successful sign-in; swallowed silently on failure so a logging error never blocks a login. |
| `last_login_addr` | no | Where that sign-in came from, stamped by the same call. Only the most recent is kept — this is a property of the account, not a log; a history belongs in the audit trail. The value is resolved through proxy headers only for hops the engine trusts, so behind an untrusted proxy it records the proxy rather than a header anybody could have written. Empty for an account that has never signed in, and for every sign-in older than the column: a sign-in that already happened has no address left to find. |
| `created_at` / `updated_at` | no | Timestamps. |

```json title="POST /api/v1/users"
{
  "username": "ada",
  "email": "ada@example.com",
  "password": "a long passphrase",
  "system_role": "user"
}
```

```json title="201 Created"
{
  "id": 42,
  "username": "ada",
  "email": "ada@example.com",
  "role": "user",
  "system_role": "user",
  "is_active": true,
  "can_view_recordings": false,
  "auth_source": "local",
  "account_type": "user",
  "created_at": "2026-08-25T10:03:11Z"
}
```

```json title="PUT /api/v1/users/:id — request (any subset of fields)"
{ "email": "ada@newdomain.example", "system_role": "admin", "can_view_recordings": true }
```

```json title="PATCH /api/v1/users/:id/status"
{ "is_active": false }
```

- **Creating** a super admin (`system_role: "superadmin"`) requires the caller to already be a super admin — `403 {"error": "only a super admin can create a super admin"}`.
- **Editing** (`PUT`) updates the mutable fields of an account: username, email, system role, password, and the recording-viewer capability. A super-admin-owned account can only be edited by another super admin (`403 {"error": "only a super admin can manage a super admin"}`). Changing a password clears the bootstrap-admin flag (`clearBootstrapAdmin`) when the target is the seeded administrator — that call is what turns off "still using the password printed at first boot".
- **Disabling** (`PATCH .../status` with `is_active: false`) stops sign-in immediately (see [The access model](model.md#disabled-accounts)) without touching grants — a suspension is reversible. A caller who tries to disable itself gets `403 {"error": "you cannot disable your own account"}`.
- **Deleting** removes the account along with its cluster grants, group memberships, machine tokens (if any), and any JIT requests it made. A caller who tries to delete itself gets `403 {"error": "you cannot delete your own account"}`.
- A caller can never disable, delete, or demote its own account, and only a super admin manages another super admin — see [Self-protection rules](model.md#self-protection-rules).
- The `/users` routes refuse a machine account with a `404 {"error": "user not found"}` — a person's affordances (a password field, a system role) do not apply to a row that has neither. Machine accounts have their own surface: [Machine accounts](machine-accounts.md).

### Federated vs. local accounts

`auth_source` says where an account's credentials actually live:

- **`local`** — the account has a bcrypt password hash in this database. Created here, edited here, and the `password` field on `PUT` is meaningful.
- **A federation provider value** (see [Single sign-on](sso.md)) — the account is vouched for by an external identity provider. `IsFederated()` is true, the row carries an `sso_provider_id` and an `external_id` (the directory's own stable identifier — an OIDC subject, a SAML NameID, an LDAP DN — used to match the account rather than its display name, because a directory is entitled to rename someone without kubemg losing track of who they are), and it has **no usable password**: password sign-in is refused for it outright rather than merely failing, and the console does not offer a password field for it (it reads `auth_source` precisely to know not to). A federated account is still provisioned as, and manageable as, an ordinary row in the same `users` table and the same `/api/v1/users` surface — the difference is entirely in how it authenticates, not in how it is granted access. Its group memberships can additionally be reconciled automatically on every login by that provider's [group mappings](sso.md#group-mappings), which a local account's memberships never are.

### Account lifecycle: create → disable → delete

| Stage | What happens | What still works | What breaks |
| --- | --- | --- | --- |
| **Create** | A row with `is_active: true` and no cluster grants. | Sign-in. Nothing else — no grants means no cluster is reachable yet. | — |
| **Disable** (`is_active: false`) | `currentUser` starts rejecting the account's JWT and its machine-token verifier on every subsequent request (see [Disabled accounts](model.md#disabled-accounts)). | Nothing for this account — a live session's next call is rejected immediately, not at token expiry. Grants, group memberships, and JIT history are untouched and restored the instant the account is re-enabled. | Sign-in, and every already-issued JWT for this account, immediately. |
| **Delete** | The row and everything that references it by foreign key are removed in one operation: cluster grants (`user_cluster_access`), group memberships, machine tokens if the row happened to be a machine account, and JIT requests the account made. | Nothing involving this account. | Any kubeconfig or machine token this account had issued stops working the next time it is presented — the identity it authenticates as no longer exists. Audit rows referencing the user id are **not** deleted; the trail keeps the numeric id so history is not rewritten. |

!!! info "Screenshot pending — `users-table.png`"
    The user list, with the grant editor open on one account.

## The access review

`GET /api/v1/users/:id/access` (admin only) answers "what can this person reach
today", which previously meant assembling five screens by hand per person: the
permission matrix for direct grants, the group list for inherited ones, the JIT
queue for live elevations, the credential register for issued kubeconfigs, and
the session index for what was actually done. The console renders it at
`/admin/users/:id`, reached by clicking a username in the list.

It is **admin-only** rather than "narrowed to your own", which is the opposite of
the rule the [audit trail](../audit/trail.md) and the credential register follow.
Those are records of things *you* did and are therefore yours to read; this is
the surface for reading *about* somebody, and reading your own would tell you
nothing `/me/access` does not already.

### What it returns, and what it does not compute twice

| Field | Notes |
| --- | --- |
| `user` | The account record, including `last_login_addr`. |
| `provider` | The identity provider a federated account signs in through, by name. `auth_source` already says *that* an account is federated; without the name an auditor has to match an id against the SSO settings page by hand. Absent for a local account, and for a federated one whose provider has since been deleted — the account is still exactly as federated either way, so this reads as absent rather than failing the review. |
| `groups` | Memberships, each with its `source`: `local` for one an administrator wrote, `sso` for one the federation sync derived. Only the derived ones are reconciled away when the directory stops asserting the group, which is a different fact about how long the access lasts. |
| `clusters` | Per cluster: the **effective** grant, and every grant that contributed to it. |

The effective grant is resolved **server-side**, by the same merge the gateway
itself performs — and this is the reason the route exists at all rather
than the console composing the permission matrix itself. A second implementation
in the browser would be free to disagree with what the proxy allows, and a review
page saying somebody holds `view` while the proxy grants `edit` is worse than no
page.

Each contributing grant carries where it came from:

- `origin: "direct"` with `source: "local"` — an administrator wrote it.
- `origin: "direct"` with `source: "sso"` — the directory asserts it.
- `origin: "direct"` with `source: "jit"` — an approved elevation, with its
  `expires_at`.
- `origin: "group"` with the `group` it was inherited through.

That distinction is the half a review actually needs: an effective
`cluster-admin` on production reads very differently depending on whether
somebody granted it in 2024, a directory asserts it, or it ends in forty minutes.

### What it deliberately leaves out

- **An expired grant.** Expiry is enforced by the resolver on every read rather
  than by the sweeper that eventually deletes the row, so a window that has run
  out is closed whether or not a background pass has run since. A review showing
  an expired elevation as live is exactly what that rule exists to prevent.
- **A grant on a cluster that no longer exists**, and a membership of a deleted
  group. Neither grants anything, and naming a row nobody can act on is noise on
  a page whose whole job is to be read line by line.
- **MFA state.** kubemg has none: a federated account's second factor is the
  identity provider's to assert and this console never sees it, and a local
  account has none to report. The page states this rather than drawing a column
  that would read "unknown" for every row.

Issued kubeconfigs and recent sessions are **not** in this response. Both already
have endpoints that narrow by user (`GET /api/v1/kubeconfigs?user_id=` and
`GET /api/v1/audit/terminal-sessions?user_id=`), and the console reads them
directly — restating two whole response shapes here would be a second place for
them to drift. Clusters are ordered production first: a review is read top-down,
and the rows that decide whether it is signed are the ones on the clusters that
matter.

## Groups

`GET|POST /api/v1/groups`, `DELETE /api/v1/groups/:id`, `POST /api/v1/groups/:id/members`, `DELETE /api/v1/groups/:id/members/:userId`.

A group is a name and a set of members; a cluster grant made against the group (via the permission matrix, below) is inherited by every current member.

```json title="POST /api/v1/groups"
{ "name": "platform-devs", "description": "Everyone on the platform team" }
```

```json title="201 Created"
{ "id": 7, "name": "platform-devs", "description": "Everyone on the platform team", "member_ids": [], "created_at": "2026-08-25T10:04:02Z" }
```

```json title="GET /api/v1/groups (excerpt)"
{ "groups": [ { "id": 7, "name": "platform-devs", "description": "Everyone on the platform team", "member_ids": [42, 43], "created_at": "2026-08-25T10:04:02Z" } ] }
```

```json title="POST /api/v1/groups/:id/members"
{ "user_id": 42 }
```

```json title="201 Created"
{ "group_id": 7, "user_id": 42 }
```

`DELETE /api/v1/groups/:id/members/:userId` takes no body and answers `204 No Content` on success, or `404 {"error": "that user is not a member of this group"}` if the pair does not exist.

Deleting a group removes its memberships and its own cluster grants in one transaction — a membership or grant referencing a deleted group would otherwise be an orphan nothing can clean up through the API.

A membership row carries a `source` the same way a cluster grant does: `local` for one an administrator added by hand, `sso` for one a federation mapping derived. A federation-derived membership is reconciled — added and removed — on every login by that provider's sync; a hand-written one is never touched by it. See [Single sign-on](sso.md#group-mappings).

## The permission matrix

`GET /api/v1/permissions`, `POST /api/v1/permissions/assign`, `POST /api/v1/permissions/revoke`.

The read returns every direct user grant and every group grant, denormalized with subject and cluster names so the console does not need three separate lookups to draw a cell:

```json title="GET /api/v1/permissions (excerpt)"
{
  "permissions": [
    {
      "subject_type": "group",
      "subject_id": 7,
      "subject_name": "platform-devs",
      "cluster_id": 3,
      "cluster_name": "prod-eu",
      "k8s_role": "edit",
      "namespaces": ["team-a"],
      "source": "local"
    }
  ]
}
```

Assigning or revoking:

```json title="POST /api/v1/permissions/assign"
{
  "subject_type": "group",
  "subject_id": 7,
  "cluster_id": 3,
  "k8s_role": "edit",
  "namespaces": ["team-a"]
}
```

`subject_type` is `user` or `group`; `k8s_role` is `view`, `edit`, or `cluster-admin`; `namespaces` omitted or empty means cluster-wide.

Revoking takes the same subject and cluster, with no role or namespace (revoking removes the whole row for that subject/cluster pair, it does not narrow it):

```json title="POST /api/v1/permissions/revoke"
{ "subject_type": "group", "subject_id": 7, "cluster_id": 3 }
```

A revoke against a subject/cluster pair with no existing grant answers `404 {"error": "that permission does not exist"}`; a successful one answers `204 No Content`.

### Worked example

Grant the `platform-devs` group `edit` on `prod-eu` scoped to `team-a`, and separately grant Ada — who is a member of that group — a direct `view` grant on the same cluster, cluster-wide:

```json
POST /api/v1/permissions/assign
{ "subject_type": "group", "subject_id": 7, "cluster_id": 3, "k8s_role": "edit", "namespaces": ["team-a"] }

POST /api/v1/permissions/assign
{ "subject_type": "user", "subject_id": 42, "cluster_id": 3, "k8s_role": "view", "namespaces": [] }
```

`GET /api/v1/permissions` now returns both rows independently — the matrix shows exactly what was assigned, one cell per subject:

```json
{
  "permissions": [
    { "subject_type": "group", "subject_id": 7, "subject_name": "platform-devs", "cluster_id": 3, "cluster_name": "prod-eu", "k8s_role": "edit", "namespaces": ["team-a"], "source": "local" },
    { "subject_type": "user", "subject_id": 42, "subject_name": "ada", "cluster_id": 3, "cluster_name": "prod-eu", "k8s_role": "view", "namespaces": [], "source": "local" }
  ]
}
```

Neither row shows Ada's *effective* access on `prod-eu` — the matrix is a table of what was granted, not a table of resolved outcomes. Ada's effective grant (per [Effective access](model.md#effective-access)) merges her direct `view`/cluster-wide row with the group's `edit`/`team-a` row into `edit`, cluster-wide — the stronger role wins, and either side being unscoped makes the merge unscoped. Revoking the group's grant later leaves Ada's direct `view` row untouched.

### A live JIT elevation in the matrix

A row carries `source` and, when it ends, `expires_at`. The console never merges a live [just-in-time elevation](jit.md) into the cell that already shows someone's standing role — that cell is what an administrator would edit to *change* standing access, and a JIT-derived row is not that. Instead a live elevation appears as a separate `+role` chip beside the standing cell: a user holding standing `view` plus a 40-minute `cluster-admin` elevation shows `view` with a `+cluster-admin` chip next to it, not a cell that silently reads `cluster-admin` and reverts on its own forty minutes later with no visible reason.

## The recording-viewing capability

**Recording visibility** is a capability, not a role: it lets an **admin** additionally replay and delete *other people's* terminal session recordings (see [Session recording](../audit/session-recording.md)). It is:

- **Admin-plus-capability** — it does nothing for a non-admin account, and a super admin holds it implicitly regardless of the stored flag.
- **Grantable only by a super admin.** The check is `recordingCapabilityDenied`, called whenever a request tries to set `can_view_recordings: true` on a create or an update. If an ordinary admin could grant it, an admin could grant it to itself, and the control would be theatre.
- Set the same way any other mutable field is: `can_view_recordings` on `POST /api/v1/users` or `PUT /api/v1/users/:id`.

### The one-time grandfather backfill

The capability was introduced after installs already had administrators relying on being able to see every recording. A one-time backfill runs at migration time and sets `can_view_recordings = true` on every existing `admin`/`superadmin` account — an upgrade must not quietly take away access somebody had yesterday. It is guarded by a stored marker.

Without that marker the backfill would re-run on every boot and re-grant what an administrator had deliberately revoked from someone. It runs exactly once, ever, per installation; every account created afterwards — including a newly promoted admin — starts without the capability and must be granted it explicitly by a super admin.
