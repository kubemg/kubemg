# Command guardrails

Block or flag specific API calls and terminal commands, even for people whose RBAC allows them. Manage them at **Admin → Settings → Guardrails** (admin only).

Normally the target cluster's own RBAC decides what someone may do ([The access model](model.md)). RBAC cannot say "an admin may do this, but not by typing it into a terminal at 03:00"; a guardrail can.

```
GET    /api/v1/guardrails              # ?cluster_id=0 asks for only the fleet-wide rules
GET    /api/v1/guardrails/templates    # the preset catalogue
POST   /api/v1/guardrails
PUT    /api/v1/guardrails/:id
DELETE /api/v1/guardrails/:id
```

## What a policy is

A regular expression matched against one of two subjects, with an action on a match.

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
| `name` | Required, at most 120 characters. |
| `description` | Optional, at most 1000 characters. Shown to administrators, never to the caller a rule refuses. |
| `cluster_id` | `0` or omitted is **fleet-wide**, including clusters registered later. A non-zero id scopes it to one cluster. An unknown cluster is refused: `400 that cluster does not exist`. |
| `pattern` | Required regular expression (RE2), at most 512 characters. See [Matching rules](#matching-rules). |
| `target` | `api_request`, `terminal_exec` or `both` (default). |
| `action` | `block` refuses the call; `warn` lets it through and records the match. Default `block`. |
| `enabled` | Default `true` on create. A disabled rule is skipped entirely. |

`GET /api/v1/guardrails` also returns `targets`, `actions` and `enforcing`, the number of rules actually running, which can be lower than the stored count if a pattern stopped compiling (see [Rule freshness across replicas](#rule-freshness-across-replicas)).

### The preset catalogue

`GET /api/v1/guardrails/templates` returns ready-made rules to apply as-is or edit. A fresh install is seeded with them **disabled**, so an upgrade never starts refusing calls overnight.

| `key` | Name | Target | Action |
| --- | --- | --- | --- |
| `delete-namespace` | Block namespace deletion | `api_request` | `block` |
| `delete-collection` | Block bulk deletion of a resource collection | `api_request` | `block` |
| `rm-rf-root` | Block `rm -rf /` in a container | `terminal_exec` | `block` |
| `fork-bomb` | Block the classic fork bomb | `terminal_exec` | `block` |
| `disk-overwrite` | Block writing directly to a block device | `terminal_exec` | `block` |
| `delete-crd` | Block deleting a CustomResourceDefinition | `api_request` | `block` |
| `delete-node` | Block deleting a Node object | `api_request` | `block` |
| `flag-secret-reads` | Flag reads of a single Secret | `api_request` | `warn` |

## Matching rules

- **`api_request`**: a proxied API call. The pattern is matched against the string `"METHOD /path"` with the query string, so `^DELETE /api/v1/namespaces/[^/?]+(\?.*)?$` means "deleting one namespace object" and not something inside it.
- **`terminal_exec`**: a command run in a container, either the argv of a non-interactive `kubectl exec`, or a line typed in an interactive shell, evaluated when Enter is pressed. The shell tracks editing keys (backspace, Ctrl-C, Ctrl-U), so a backspaced character is not matched. Lines are buffered up to 8 KB.

A pattern must not match the empty string. `.*`, `.?` and `^` are refused at save, because a `block` rule that matches everything would lock everyone out of the console that could undo it. The matched subject is truncated at 4096 characters.

**Evaluation order:** fleet-wide rules are checked before a cluster's own. A `block` wins immediately. A `warn` is remembered but evaluation continues, so a cluster-specific `block` is never masked by a fleet-wide rule that only observes.

## Three worked policies

### Block `exec` into production

To stop a shell opening at all, use an `api_request` rule, because `terminal_exec` only sees command text and never the act of opening the session. Scope it to the production cluster and match the exec/attach path:

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

Reading pods, tailing logs and editing a ConfigMap are untouched.

### Block deletes in one namespace

Useful for a change freeze. There is deliberately no tail anchor: it catches every delete of anything *inside* `payments-prod`, unlike the `delete-namespace` template.

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

### A read-only window

A rule has no clock, so create it ahead of time disabled and enable it for the freeze:

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

`PUT /api/v1/guardrails/:id` replaces the whole policy, so switching it on or off is the same call with only `enabled` flipped. Once enabled, every write on that cluster is refused, whatever role approved it.

## How a blocked call is reported

**kubectl (API request):** the call never reaches the cluster. The caller gets a `403` that distinguishes kubemg from the cluster's RBAC:

```json
{
  "error": "Blocked by kubemg Safety Policy: Block namespace deletion",
  "guardrail_blocked": true,
  "policy": "Block namespace deletion",
  "scope": "global"
}
```

**Interactive shell:** the refusal prints in red on the operator's terminal (*Blocked by kubemg Safety Policy: ...*) and the typed line is cleared from the remote shell so the next Enter cannot run it.

## And to the audit trail

Every match, `block` or `warn`, is recorded, so running a rule in `warn` for a week and reading the trail is a real way to gain confidence before enabling `block`. A matched command in an interactive session gets **its own audit row**, since the cluster never saw a blocked one. The typed text is deliberately **not** stored on that row (it may hold a mistyped password); it is in the [session recording](../audit/session-recording.md), behind the capability that governs watching one.

## What a guardrail is not

It is not a sandbox. Anyone with a shell can defeat a pattern with a variable, a base64 pipe or an editor. The real protection against a determined insider is the grant that let them in, plus the recording. What a guardrail stops is the common failure: the right command typed against the wrong cluster.

## Rule freshness across replicas

Rules are compiled into an in-memory snapshot read without a database call per request. It is refreshed at boot, after every write and every 30 seconds (which is how other replicas pick up a change). A rule whose pattern fails to compile is skipped and logged. If a scheduled refresh cannot read the database, the **previous** rules stay in force, so an outage never leaves the fleet unguarded.
