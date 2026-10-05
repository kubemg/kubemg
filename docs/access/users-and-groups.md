# Users and groups

Create accounts, put them in groups and give users or groups access to clusters. Everything here is admin-only, under **Admin → Users**, **Admin → Groups** and **Admin → Permissions**.

## Users

<figure markdown>
  ![The user list](../assets/screenshots/users-table.png)
  <figcaption>Admin → Users. System role and status change in the row itself; an account's name opens its own page, where the access review lives.</figcaption>
</figure>

Routes: `GET|POST /api/v1/users`, `PUT|DELETE /api/v1/users/:id`, `PATCH /api/v1/users/:id/status`. See the [REST API reference](../dev/api.md).

### The user record

| Field | Writable? | Notes |
| --- | --- | --- |
| `id` | no | Assigned on creation. |
| `username` | yes | Unique; a conflict answers `409 username already taken`. No `:` or control characters. |
| `email` | yes | Optional; local accounts are keyed by username. |
| `password` | write-only | Never read back. Minimum 8 characters. |
| `system_role` | yes, conditionally | `superadmin`, `admin` or `user`. Setting `superadmin` needs a super admin caller; nobody can change their own. |
| `role` | no | Derived from `system_role`, see [System roles](model.md#system-roles). |
| `is_active` | via `PATCH .../status` only | Not settable through `PUT`. |
| `can_view_recordings` | yes, conditionally | Super admin only, see [The recording-viewing capability](#the-recording-viewing-capability). |
| `account_type` | no | `user` or `machine`. The `/users` routes never produce or accept a machine row. |
| `auth_source` | no | `local` or a federation provider, see [Federated vs. local accounts](#federated-vs-local-accounts). |
| `last_login_at`, `last_login_addr` | no | Most recent sign-in and where it came from. Only the latest is kept (history is in the [audit trail](../audit/trail.md)); empty for an account that has never signed in. |
| `created_at` / `updated_at` | no | Timestamps. |

```json title="POST /api/v1/users"
{
  "username": "ada",
  "email": "ada@example.com",
  "password": "a long passphrase",
  "system_role": "user"
}
```

Responses: `201 Created` with the record (no password). `PUT` takes any subset of fields; `PATCH .../status` takes `{ "is_active": false }`.

- **Creating or editing a super admin** needs a super admin caller: `403 only a super admin can create a super admin` / `... can manage a super admin`.
- **Changing the seeded administrator's password** turns off the "still using the first-boot password" warning.
- **Disabling** stops sign-in immediately and leaves grants untouched, so it is reversible. Disabling yourself: `403 you cannot disable your own account`.
- **Deleting** removes the account with its grants, group memberships, machine tokens and JIT requests. Deleting yourself: `403 you cannot delete your own account`.
- Machine accounts answer `404 user not found` on these routes; they live on [Machine accounts](machine-accounts.md).

See [Self-protection rules](model.md#self-protection-rules).

### Federated vs. local accounts

`auth_source` says where credentials live.

- **`local`**: a password hash in this database. Created and edited here.
- **A federation provider** (see [Single sign-on](sso.md)): the identity provider vouches for the account. It has no usable password (password sign-in is refused and the console shows no password field) and is matched by the directory's stable identifier, so a rename in the directory does not lose the account. Otherwise it is managed like any other row, and its group memberships can be reconciled on every login by the provider's [group mappings](sso.md#group-mappings).

### Account lifecycle: create → disable → delete

| Stage | What happens | What breaks |
| --- | --- | --- |
| **Create** | Active account, no grants. It can sign in but reach no cluster. | Nothing. |
| **Disable** | Every request from the account is rejected at once, including live sessions and machine tokens. Grants, memberships and JIT history are kept and restored on re-enable. | Sign-in and every issued session token. |
| **Delete** | The account and everything referencing it are removed together. | Any kubeconfig or machine token it issued stops working when next presented. Audit rows are kept, with the numeric id. |

### Your own profile

Everyone has a profile page at `/me/profile`, reached by clicking your name on the card at the bottom of the sidebar (on a narrow screen, **You → My profile**; also in ⌘K). It shows your username, role, email, how you sign in, your last sign-in and any capability you were granted, and offers two acts:

- **Edit profile** changes your **email** only (empty removes it). The username is the name clusters and audit records see, so renaming stays an administrator's edit.
- **Change password** is the same sheet as on **My credentials**; it can revoke your kubeconfigs with the rotation. See [Rotating a password can take them with it](kubeconfigs.md#rotating-a-password-can-take-them-with-it).

Both are absent for federated accounts (the provider owns email and password) and machine accounts; the API answers `409` with the reason. An email edit is audited as `profile-update`. On a short screen (under 960 pixels tall) **My access** and **My credentials** fold behind the chevron beside your name.

## The access review

Open an account's name in the user list (`/admin/users/:id`) to see what that person can reach today. It replaces assembling the matrix, group list, JIT queue, credential register and session index by hand. Admin only, because it reads about somebody else.

| Field | Notes |
| --- | --- |
| `user` | The account record. |
| `provider` | The identity provider a federated account signs in through; absent for a local account or one whose provider was deleted. |
| `groups` | Memberships with their `source`: `local` (written by an administrator) or `sso` (derived; removed automatically when the directory stops asserting the group). |
| `clusters` | Per cluster, production first: the **effective** grant and every grant that contributed to it. |

The effective grant is computed server-side by the same merge the gateway uses, so the page cannot disagree with what the proxy allows. Each contributing grant shows where it came from:

- `direct` + `local`: an administrator wrote it.
- `direct` + `sso`: the directory asserts it.
- `direct` + `jit`: an approved elevation, with its `expires_at`.
- `group`: inherited through the named group.

Left out on purpose: expired grants, grants on deleted clusters and memberships of deleted groups (nothing to act on), and MFA state (kubemg has none; a federated account's second factor is the provider's). Issued kubeconfigs and recent sessions are read from their own endpoints (`GET /api/v1/kubeconfigs?user_id=`, `GET /api/v1/audit/terminal-sessions?user_id=`).

## Groups

Routes: `GET|POST /api/v1/groups`, `DELETE /api/v1/groups/:id`, `POST /api/v1/groups/:id/members`, `DELETE /api/v1/groups/:id/members/:userId`.

A group is a name and a set of members. A cluster grant made against the group is inherited by every current member.

```json title="POST /api/v1/groups"
{ "name": "platform-devs", "description": "Everyone on the platform team" }
```

```json title="POST /api/v1/groups/:id/members"
{ "user_id": 42 }
```

Removing a member answers `204`, or `404 that user is not a member of this group`. Deleting a group removes its memberships and its own cluster grants in one transaction.

A membership has a `source` like a grant: `local` (added by hand) or `sso` (derived from a federation mapping). The provider's sync adds and removes only `sso` memberships and never touches hand-written ones. See [Single sign-on](sso.md#group-mappings).

## The permission matrix

Routes: `GET /api/v1/permissions`, `POST /api/v1/permissions/assign`, `POST /api/v1/permissions/revoke`.

The read returns every direct and group grant with subject and cluster names. It shows what was **granted**, not resolved outcomes; see [Effective access](model.md#effective-access) for those.

```json title="POST /api/v1/permissions/assign"
{
  "subject_type": "group",
  "subject_id": 7,
  "cluster_id": 3,
  "k8s_role": "edit",
  "namespaces": ["team-a"]
}
```

`subject_type` is `user` or `group`; `k8s_role` is `view`, `edit` or `cluster-admin`; omitted or empty `namespaces` means cluster-wide.

Revoke takes `subject_type`, `subject_id` and `cluster_id` only. It removes the whole row for that pair and does not narrow it. A pair with no grant answers `404 that permission does not exist`; success is `204`.

Example: grant `platform-devs` `edit` on `prod-eu` scoped to `team-a`, and grant Ada (a member) a direct cluster-wide `view`. The matrix shows both rows. Ada's effective access is cluster-wide `edit`. Revoking the group grant leaves her `view` row untouched.

### A live JIT elevation in the matrix

A live [just-in-time elevation](jit.md) is never merged into the cell showing someone's standing role. It appears as a separate `+role` chip beside it (standing `view` plus a 40-minute elevation shows `view` with `+cluster-admin`), so nothing silently reverts later.

## The recording-viewing capability

A capability, not a role: it lets an **admin** also replay and delete *other people's* terminal recordings (see [Session recording](../audit/session-recording.md)).

- It does nothing for a non-admin; a super admin holds it implicitly.
- Only a **super admin** can grant it, otherwise an admin could grant it to themselves. Set `can_view_recordings` on `POST /api/v1/users` or `PUT /api/v1/users/:id`.
- On upgrade, existing admins and super admins were granted it once so nobody lost access they had. Accounts created or promoted afterwards start without it.
