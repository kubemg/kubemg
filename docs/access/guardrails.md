# Command guardrails

Everywhere else in kubemg, "may this person do this?" is answered by the target cluster's own RBAC through impersonation — see [The access model](model.md). A guardrail is the one control that is deliberately **not** that. It is not about privilege: the people a guardrail stops are usually the ones who genuinely hold it, which is exactly why `kubectl delete ns prod` succeeds against RBAC. RBAC has no way to express "an admin may do this, but not by typing it into a terminal at 03:00" — that is the whole of what a guardrail says.

Administered at **Admin → Settings → Guardrails** (`GuardrailSettingsPanel.tsx`), API under `/api/v1/guardrails`, all admin-only:

```
GET    /api/v1/guardrails              # ?cluster_id=0 asks for only the fleet-wide rules
GET    /api/v1/guardrails/templates    # the preset catalogue
POST   /api/v1/guardrails
PUT    /api/v1/guardrails/:id
DELETE /api/v1/guardrails/:id
```

## What a policy is

A `GuardrailPolicy` is a regular expression matched against one of two subjects, with an action taken on a match:

```json title="POST /api/v1/guardrails"
{
  "name": "Block namespace deletion",
  "description": "Deleting a namespace deletes everything in it.",
  "cluster_id": 0,
  "pattern": "^DELETE /api/v1/namespaces/[^/?]+(\\?.*)?$",
  "target": "api_request",
  "action": "block"
}
```

| Field | Notes |
| --- | --- |
| `name` | Required, trimmed, at most 120 characters (`maxGuardrailNameLength`). An empty or over-length name is refused with `400`. |
| `description` | Optional, at most 1000 characters (`maxGuardrailDescriptionLength`). Free text shown to whoever administers the rule later — it is never shown to the caller a rule refuses. |
| `cluster_id` | `0` (or omitted) is **fleet-wide** — it applies to every registered cluster, including one registered *after* the rule was written, which no per-cluster rule can keep up with. A non-zero id scopes the rule to one cluster, which is what lets production be stricter than a sandbox. A `cluster_id` naming a cluster that does not exist is refused with `400 {"error": "that cluster does not exist"}` at write time, rather than being stored as a rule that enforces nothing while still reading as active. |
| `pattern` | Required. A regular expression (RE2 syntax, since it compiles with Go's `regexp` package), at most 512 characters, validated by `ValidatePattern` — see [Matching rules](#matching-rules). |
| `target` | `api_request`, `terminal_exec`, or `both` (`db.GuardrailTargets`). Empty defaults to `both` on write. See [Matching rules](#matching-rules) below. |
| `action` | `block` refuses the call outright; `warn` lets it through and records that the rule matched, without stopping anything (`db.GuardrailActions`). Empty defaults to `block` on write. |
| `enabled` | Defaults to `true` on create; honored as sent on update. A disabled rule is skipped at compile time — it does not slow anything down and does not appear as enforced. |

`GET /api/v1/guardrails` also returns `targets` and `actions` (the two enums above, so a client never hard-codes them) and `enforcing` — the rule count the gateway's own compiled snapshot is actually running, which can legitimately be lower than the stored count if a pattern stopped compiling (see [Rule freshness across replicas](#rule-freshness-across-replicas)).

### The preset catalogue

`GET /api/v1/guardrails/templates` returns a fixed set of pre-written rules an administrator can apply as-is or edit before saving — the empty rule list with a blank pattern box is a feature nobody turns on, since a guardrail pattern is a regular expression matched against a subject most operators have never had to write down. Each template (`db.GuardrailTemplate`) carries a stable `key`, plus the same `name`/`description`/`pattern`/`target`/`action` fields a stored policy does, so applying one is filling a form from the response rather than a second write shape. The full catalogue (`db.GuardrailTemplates`), in the order the endpoint returns them:

| `key` | Name | Target | Action | Pattern |
| --- | --- | --- | --- | --- |
| `delete-namespace` | Block namespace deletion | `api_request` | `block` | `^DELETE /api/v1/namespaces/[^/?]+(\?.*)?$` |
| `delete-collection` | Block bulk deletion of a resource collection | `api_request` | `block` | `^DELETE /api(/v[0-9a-z]+\|s/[^/]+/v[0-9a-z]+)/namespaces/[^/]+/[a-z]+(\?\|$)` |
| `rm-rf-root` | Block `rm -rf /` in a container | `terminal_exec` | `block` | Matches `rm` with any ordering/casing of `-r`/`-f` flags followed by `/` |
| `fork-bomb` | Block the classic fork bomb | `terminal_exec` | `block` | Matches `:(){ :\|:& };:` and its variations |
| `disk-overwrite` | Block writing directly to a block device | `terminal_exec` | `block` | Matches `dd ... of=` or `mkfs` against `/dev/sd*`, `/dev/nvme*`, `/dev/xvd*`, `/dev/vd*` |
| `delete-crd` | Block deleting a CustomResourceDefinition | `api_request` | `block` | `^DELETE /apis/apiextensions\.k8s\.io/v[0-9a-z]+/customresourcedefinitions/` |
| `delete-node` | Block deleting a Node object | `api_request` | `block` | `^DELETE /api/v1/nodes/` |
| `flag-secret-reads` | Flag reads of a single Secret | `api_request` | `warn` | `^GET /api/v1/namespaces/[^/]+/secrets/[^/?]+` |

These are also what a fresh install is seeded with — **disabled** — so an upgrade never starts silently refusing calls an operator made yesterday.

## Matching rules

A guardrail watches one, or both, of two genuinely different subjects:

- **`api_request`** — a proxied Kubernetes API call. The subject the pattern is matched against is the literal string `"METHOD /path"`, query string included — so `^DELETE /api/v1/namespaces/[^/?]+(\?.*)?$` reads exactly as "deleting one namespace object" and (thanks to the trailing anchor) does **not** also match deleting something *inside* a namespace.
- **`terminal_exec`** — a command run inside a container: either the argv of a non-interactive `kubectl exec -- ...` (read straight off the `command` query parameter of the exec request, since it never gets typed anywhere), or a line an operator types into an interactive shell, evaluated **as it is typed**, character by character, and matched at each newline.

The interactive half is a line editor, not a passive tap: it accumulates keystrokes, honors the terminal's own editing keys (backspace, Ctrl-C, Ctrl-U, delete, escape sequences) so that a backspaced-out character is not still part of what gets matched, and evaluates the buffered line the moment Enter is pressed — buffered lines are capped at 8 KB so a very long paste is still checked rather than growing without limit.

A pattern must not match the empty string — `ValidatePattern` refuses `.*`, `.?`, `^`, and similarly unconditional patterns at write time, because a `block` rule that matches everything is a fleet nobody can reach through kubemg, entered by typing two characters into a text field, and the person who would have to undo it is locked out of the same console. Patterns are capped at 512 characters and the subject matched against is truncated at 4096 characters — RE2 has no catastrophic-backtracking failure mode, so this is about keeping a rule readable rather than about safety.

**Evaluation order**: global (fleet-wide) rules are checked before a cluster's own. A `block` anywhere wins immediately and stops evaluation — once one rule refuses the call, no other rule can un-refuse it. A `warn` match is remembered but evaluation continues, so a cluster-specific `block` is never masked by a fleet-wide rule that only observes.

## Three worked policies

### Block `exec` into production

This has to be an `api_request` rule, not a `terminal_exec` one: a `terminal_exec` target is matched only against the *command text* — either a non-interactive exec's argv or a line typed into an already-open shell — never against the act of opening the session itself (`p.guardAPIRequest` runs on the exec/attach request before `p.guardCommand` ever sees a command). To refuse the shell from opening at all, scope the rule to the production cluster's own `id` and match the exec/attach subresource in the request path:

```json title="POST /api/v1/guardrails"
{
  "name": "No shells in production",
  "description": "Debugging happens against logs and describe, not a live shell in prod.",
  "cluster_id": 9,
  "pattern": "/pods/[^/]+/(exec|attach)(\\?.*)?$",
  "target": "api_request",
  "action": "block"
}
```

Because this fires in `guardAPIRequest` before the request ever reaches the tunnel, the caller never gets far enough to type anything — there is no shell to police a keystroke inside. Everything else (reading pods, tailing logs, editing a ConfigMap) is untouched, since the pattern only matches the exec/attach path.

### Block deletes in one namespace

Fleet-wide in principle, but the pattern itself is what narrows it to a single namespace — useful for a namespace under a change freeze without touching the rest of the cluster it lives on:

```json title="POST /api/v1/guardrails"
{
  "name": "No deletes in payments-prod during the freeze",
  "description": "Change freeze for the payments launch. Remove or disable after it lifts.",
  "cluster_id": 0,
  "pattern": "^DELETE /api/v1/namespaces/payments-prod/",
  "target": "api_request",
  "action": "block"
}
```

Note the deliberate **absence** of a tail anchor here, unlike the `delete-namespace` template: this rule wants to catch every delete of anything *inside* `payments-prod` (a pod, a Deployment, a Secret), not just a delete of the namespace object itself — the opposite anchoring choice from the namespace-deletion preset, for the opposite reason.

### A read-only window

There is no clock in a `GuardrailPolicy` — a rule is either `enabled` or it is not, with no schedule of its own — so a temporary read-only window is built by toggling a rule that blocks every mutating verb, created ahead of time and flipped on for the freeze:

```json title="POST /api/v1/guardrails"
{
  "name": "Freeze: no writes cluster-wide",
  "description": "Enable for the deploy freeze window, disable the moment it lifts.",
  "cluster_id": 3,
  "pattern": "^(POST|PUT|PATCH|DELETE) /",
  "target": "api_request",
  "action": "block",
  "enabled": false
}
```

Created with `enabled: false` so it can be reviewed and left in place between freezes. `PUT /api/v1/guardrails/:id` is a full replace rather than a patch — `guardrailFrom` re-validates every field from the request body, so the on-switch and off-switch are the same call, resending the whole policy with only `enabled` flipped between `true` and `false`. Because evaluation checks global rules before cluster ones and a `block` anywhere stops evaluation immediately, this rule refuses every write on the named cluster the instant it is enabled, regardless of what role approved the call upstream.

## How a blocked call is reported

**To kubectl (an API request):** the proxied call never reaches the tunnel. The caller gets a `403` with a body that distinguishes a kubemg refusal from the cluster's own RBAC saying no:

```json
{
  "error": "Blocked by kubemg Safety Policy: Block namespace deletion",
  "guardrail_blocked": true,
  "policy": "Block namespace deletion",
  "scope": "global"
}
```

**To an interactive shell (a typed command):** the refusal shows on the operator's own terminal, on stderr — because it is the gateway talking *about* the session, not the container talking inside it — and the buffered command is cleared out of the remote shell's line editor before it can run. Suppressing the Enter alone would leave every character of the refused command still sitting in the remote shell's own input buffer, one keypress from running on the *next* Enter; a `Ctrl-U` frame is sent immediately after the refusal to actually clear it.

```
\x1b[1;31mBlocked by kubemg Safety Policy: Block `rm -rf /` in a container\x1b[0m
```

## And to the audit trail

Every match — `block` or `warn` — is recorded. A `warn` match is recorded specifically so that running a rule in warn mode for a week and reading the trail is a real workflow: a rule nobody dares enable in `block` is worth less than one that has been quietly observing and can be turned on with confidence. For an interactive session, a matched (and possibly blocked) command gets **its own audit row**, separate from the session's own open/close records — a session is recorded twice regardless of what happened inside it, so without a row of its own, a command refused at 03:00 would leave no trace anywhere: the cluster never saw it (it was blocked before reaching the tunnel), and the session's two rows just describe a shell where nothing looks unusual.

What was actually typed is **deliberately not** stored on that audit row — the trail is queried far more broadly than a session recording is, and a refused command line is exactly the kind of text that holds a mistyped password. The full command is captured in the [session recording](../audit/session-recording.md) instead, behind the capability that governs watching one.

## What a guardrail is not

A keystroke guard is not a sandbox, and is not sold as one here. Anyone who can open a shell can defeat a pattern with a variable, a base64 pipe, or a text editor — no amount of pattern matching changes that; the actual protection against a determined insider is the grant that let them in, plus the fact that the session is recorded. What this control does stop is the far more common failure: the right command typed against the wrong cluster, by someone who fully intended to run it somewhere else and would have caught the mistake immediately if the console had said something.

## Rule freshness across replicas

Guardrail rules are compiled from the database into an in-memory snapshot (`guardrails.Compile`) and published to the gateway; the hot path reads that snapshot lock-free rather than taking a database round trip per call. The snapshot is republished at boot, after every write through the admin API, and on a 30-second timer — the timer is what lets a second replica pick up a rule change made through its sibling. A policy whose pattern fails to compile is skipped and logged rather than failing the whole publish, and a database read failure during a scheduled refresh leaves the **previous** rule set in force rather than clearing it — a transient outage must never turn into an unguarded fleet.
