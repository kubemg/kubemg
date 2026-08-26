# Machine accounts

A release pipeline needs a kubeconfig too, and neither existing credential fit it: a person's session is tied to someone logging in, and a generated kubeconfig's proxy-scoped JWT is right for a file that lives on a laptop for a day, not for a secret sitting in a CI store for months. A **machine account** is the third shape — issued at **Admin → Identity → Machine Accounts** (`MachineAccounts.tsx`, `/admin/machine-accounts`).

## What it is

A machine account is an ordinary `db.User` row with `AccountType = "machine"`. That is the load-bearing design choice: every grant, every namespace scope, the permission matrix, the audit trail, and the proxy's impersonation are all keyed on a user id, so a machine account needs the exact same access model a person does rather than a second shape bolted alongside it. Two things differ, and both are enforced in the model itself rather than in a handler:

- **It holds no password**, and login refuses it the same way a federated account is refused — as an unknown username, so accounts cannot be enumerated by probing `/auth/login`. See [Single sign-on: account enumeration](sso.md#account-enumeration).
- **`Normalize()` pins it to `SystemRoleUser`.** A row edited by hand in the database cannot smuggle admin access onto a credential that lives in a CI secret store.

The name is validated more strictly than a person's username, because it is sent to the target cluster as `Impersonate-User` and has to be something a Kubernetes RoleBinding can name and an operator reading `kubectl auth can-i --as` output recognizes:

```go
var machineAccountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$`)
```

```
POST /api/v1/machine-accounts
{ "username": "jenkins-release", "email": "platform-team@example.com" }
```

`email` names the owner — who to ask about a credential nobody recognizes — which is what makes an abandoned token actionable rather than merely visible in a list.

## Issuing a token

```
POST /api/v1/machine-accounts/:id/tokens
{ "name": "release pipeline", "cluster_id": 3, "namespace": "team-a", "ttl_seconds": 7776000 }
```

The secret is 256 bits of CSPRNG output, prefixed `kmgm_`. Only its SHA-256 hash is stored (`machine_tokens.token_hash`) — there is nothing to guess, and a password KDF would only add work factor to the hot path every proxied call goes through. It is shown **once**:

```json title="201 Created"
{
  "token": { "id": 12, "hint": "kmgm_7f3a2b", "cluster_id": 3, "expires_at": "2027-08-22T00:00:00Z", "status": "active" },
  "secret": "kmgm_THE-SECRET-IS-SHOWN-HERE-ONCE",
  "kubeconfig": "apiVersion: v1\n...",
  "filename": "prod-eu-jenkins-release.kubeconfig",
  "context": "prod-eu",
  "server": "https://kubemg.example.com/api/v1/clusters/3/proxy",
  "k8s_role": "edit",
  "warning": ""
}
```

`hint` is the token's own opening characters — enough for a CI system holding the secret and this console holding a row to agree on which one they are talking about without either side guessing.

### The TTL ladder and `never_expires`

```go
const (
    defaultMachineTokenTTL = 90 * 24 * time.Hour     // a quarter, if nothing is said
    maxMachineTokenTTL     = 10 * 365 * 24 * time.Hour // a guard against a typo, not a policy
)
```

The ladder in `MachineTokenSheet.tsx` runs longer than the human kubeconfig ladder — starting at a day and going past a year — because a credential meant to sit in a secret store for the life of a pipeline is a different object than one meant to sit on a laptop for a shift.

A credential with **no expiry at all** is allowed — a release pipeline that stops working at 3am on a quarter boundary is an outage nobody scheduled — but it must be asked for explicitly:

```json
{ "name": "release pipeline", "cluster_id": 3, "never_expires": true }
```

`ttl_seconds` and `never_expires` cannot both be set (`"a token either expires or it does not"`). Choosing `never_expires` is disclosed back in the response's `warning`:

> This credential never expires. It stops working when it is revoked here or the machine account is disabled, so review it against its last-used time rather than waiting for a clock.

`last_used_at` is what replaces the clock as the control: it is written at most once every five minutes (off the request's own path, so a pipeline listing pods in a loop does not turn one indexed lookup into a write per call), and a token with an old `last_used_at` and no expiry is exactly what to go prune.

## Revoking

```
DELETE /api/v1/machine-accounts/:id/tokens/:tokenId
```

The row is kept, marked `revoked_at`, rather than deleted — "what existed and when did it stop" is the question an auditor asks about a credential, and revoking takes effect on the next call, not at some future expiry. Deleting the whole machine account takes every one of its tokens with it, since a row nothing resolves is a credential nobody can find or revoke.

Disabling the account (`PATCH /api/v1/machine-accounts/:id/status`) is the blunt lever — every token the account holds stops working at once — where revoking one token stops one pipeline.

## The four refusals

Issuing a token is refused outright in four cases, each because the alternative would hand out a credential nobody could actually use or actually withdraw:

1. **Direct mode is refused.** `"programmatic access needs a cluster registered in agent mode. In direct mode the credential is minted on the cluster itself, so kubemg cannot revoke it and the cluster's RBAC has nothing bound to it."` There, revoking a machine token here does nothing to the actual credential a pipeline holds.
2. **No grant on the cluster is refused.** `"this machine account has no access to that cluster yet — grant it a role first, otherwise the credential authenticates and is then refused by the cluster."` Grant the account a role via the permission matrix before issuing its first token.
3. **A namespace outside the account's grant is refused** rather than silently substituted — the same `resolveNamespace` check every namespace-scoped kubeconfig and permission read goes through.
4. **A token for a human account is refused at verification** (`machineTokenVerifier.VerifyMachineToken` checks `user.IsMachine()`), even though nothing about the token format itself distinguishes the two — this closes the case of a database row edited by hand into looking like a machine token.

## How a pipeline uses it

The issued `kubeconfig` in the response is a complete, ready-to-use file — write it to disk (or, more realistically, store `secret` in the CI system's own secret store and reconstruct the file at run time from the same template) and run `kubectl` against it directly:

```yaml title="Example: GitLab CI job using a machine account's kubeconfig"
deploy:
  stage: deploy
  script:
    - echo "$KUBEMG_KUBECONFIG" > kubeconfig.yaml
    - kubectl --kubeconfig kubeconfig.yaml apply -f deploy/
  variables:
    KUBECONFIG: kubeconfig.yaml
```

Where `KUBEMG_KUBECONFIG` is a protected CI variable holding the `kubeconfig` field from the issuance response (or the file rendered from `secret` at deploy time — whichever fits the pipeline's own secret-handling practice better; kubemg does not distinguish the two, since revocation is keyed on the token hash, not on which form of the file it ends up in).

The credential rides the same impersonated, audited tunnel as a person's session — the account's grant on the cluster is what decides what `kubectl apply` in that job may actually do, enforced by the target cluster's own RBAC exactly as for a human `edit` grant.

## Audit records

Issuing and revoking a token are recorded as `machine-token-issue` / `machine-token-revoke`, with the identities **crossed** the same way a recording replay's are: the record's `user_id`/`username` is the **administrator** who acted, and `impersonated_user` names the **machine account** the token speaks for — because neither half alone answers "who issued production access to what."

```json title="One audit row for a token issuance"
{
  "verb": "machine-token-issue",
  "username": "ada",
  "impersonated_user": "jenkins-release",
  "cluster": "prod-eu",
  "resource": "servicetokens",
  "status": 201
}
```
