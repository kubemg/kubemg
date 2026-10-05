# Machine accounts

Give a pipeline or other unattended job its own kubeconfig, with a long-lived revocable token. Issue them at **Admin → Identity → Machine Accounts** (`/admin/machine-accounts`).

A person's session is tied to a login, and a generated kubeconfig suits a laptop for a day, not a CI secret store for months. A machine account is the third shape.

## What it is

A machine account is an ordinary account whose type is `machine`, so it uses the same grants, namespace scopes, permission matrix, audit trail and impersonation as a person. Two differences:

- **It has no password.** Login refuses it as an unknown username, so accounts cannot be enumerated (see [account enumeration](sso.md#account-enumeration)).
- **It is always the `user` system role**, so it can never be made an admin.

The name is sent to the cluster as `kubemg:u:<name>` (see [Why the username is prefixed](model.md#why-the-username-is-prefixed)) and must match:

```
^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$
```

```
POST /api/v1/machine-accounts
{ "username": "jenkins-release", "email": "platform-team@example.com" }
```

`email` names the owner, so an abandoned token can be traced to someone.

## Issuing a token

```
POST /api/v1/machine-accounts/:id/tokens
{ "name": "release pipeline", "cluster_id": 3, "namespace": "team-a", "ttl_seconds": 7776000 }
```

The secret (prefix `kmgm_`) is shown **once** and only its hash is stored. The response also carries a ready kubeconfig, the filename, context, server, role and any `warning`; the token's `hint` (its first characters) lets a CI system and this console agree on which token is which.

```json title="201 Created"
{
  "token": { "id": 12, "hint": "kmgm_7f3a2b", "cluster_id": 3, "expires_at": "2027-08-22T00:00:00Z", "status": "active" },
  "secret": "kmgm_THE-SECRET-IS-SHOWN-HERE-ONCE",
  "kubeconfig": "apiVersion: v1\n...",
  "server": "https://kubemg.example.com/api/v1/clusters/3/proxy",
  "k8s_role": "edit"
}
```

### Lifetime and `never_expires`

| Bound | Value |
| --- | --- |
| Default | 90 days |
| Longest | 10 years (a guard against typos, not a policy) |

A token with **no expiry** is allowed, but you must ask for it with `"never_expires": true`. It cannot be combined with `ttl_seconds` (`a token either expires or it does not`), and the response warns that it ends only when revoked or the account is disabled. Review such tokens by their **last used** time, which is updated at most every five minutes; an old value with no expiry is what to prune.

## Revoking

`DELETE /api/v1/machine-accounts/:id/tokens/:tokenId` revokes one token from the next call. The row is kept, marked revoked. Deleting the account deletes all its tokens. Disabling it (`PATCH /api/v1/machine-accounts/:id/status`) stops every token at once.

## The four refusals

1. **Direct mode.** `programmatic access needs a cluster registered in agent mode...` In direct mode the cluster mints the credential, so kubemg could not revoke it.
2. **No grant on the cluster.** Give the account a role in the permission matrix before issuing its first token.
3. **A namespace outside the account's grant** is refused, not substituted.
4. **A token for a human account** is refused when presented, even if it looks like a machine token.

## How a pipeline uses it

Store the `kubeconfig` (or the `secret`, to rebuild it) in the CI secret store and run `kubectl` against it. The credential rides the same impersonated, audited tunnel as a person's; the account's grant, enforced by the cluster's own RBAC, decides what it may do.

```yaml title="GitLab CI"
deploy:
  stage: deploy
  script:
    - echo "$KUBEMG_KUBECONFIG" > kubeconfig.yaml
    - kubectl --kubeconfig kubeconfig.yaml apply -f deploy/
```

## Audit records

Issuing and revoking are recorded as `machine-token-issue` / `machine-token-revoke`. The record's user is the **administrator** who acted and `impersonated_user` is the **machine account**, so one row answers "who issued production access to what".
