# Runtime settings

The settings an administrator can change at runtime from the Settings pages,
without a redeploy, with their defaults, ranges and refusals. Both
`GET /api/v1/settings` and `PUT /api/v1/settings` are admin-only; see the
[REST API](../dev/api.md).

## Resolution rule

Every setting below follows the same rule:

1. The **environment variable** supplies the boot-time default.
2. A **stored override**, written through `PUT /api/v1/settings`, takes
   effect at the next read and wins over the default.
3. **An empty override means "use the default"** — that is how a setting is
   cleared. Sending `""` (or, for a numeric setting, `0`) removes the
   override rather than storing an empty value.
4. If the database cannot be reached, the **boot-time environment value** is used
   rather than failing the request.

`GET /api/v1/settings` returns all three views at once:

```json
{
  "effective": { "...": "what the server is actually using right now" },
  "overrides": { "...": "what is stored in the database; empty means the default applies" },
  "defaults":  { "...": "the environment-supplied fallback, so clearing a field shows what it restores" },
  "warnings":  ["..."]
}
```

## Settings

### `public_url`

| | |
|---|---|
| Meaning | The address a **target cluster** must be able to reach this bastion at. Baked into every generated agent install command and manifest, and into every generated agent kubeconfig's server address. |
| Environment default | `KUBEMG_PUBLIC_URL` (falls back to `http://localhost:8080` if unset) |
| Validation | Must be an absolute `http://` or `https://` address with a host |
| Unset behaviour | n/a — always resolves to the environment default when cleared |

This is the outside view of the bastion, not its listen address. A loopback or
private value means an agent inside a target cluster can never dial back in.

### `agent_image`

| | |
|---|---|
| Meaning | The container image installed into a target cluster when it registers in agent mode. |
| Environment default | `KUBEMG_AGENT_IMAGE` (falls back to the build's own default, currently `ghcr.io/kubemg/kubemg-agent:0.13.0`) |
| Validation | none beyond trimming |

### `agent_namespace`

| | |
|---|---|
| Meaning | The namespace the agent is installed into on a target cluster. |
| Environment default | `KUBEMG_AGENT_NAMESPACE` (falls back to `kubemg-system`) |
| Validation | Must be a valid Kubernetes name (lowercase letters, digits, dashes; not leading/trailing dash) if non-empty |

### `agent_image_pull_secret`

| | |
|---|---|
| Meaning | The name of a `docker-registry` Secret, in the agent namespace on each target cluster, that the agent and browser shell images are pulled with — for a mirror that requires authentication. |
| Environment default | `KUBEMG_AGENT_IMAGE_PULL_SECRET` (falls back to none) |
| Validation | Must be a valid Secret name (lowercase letters, digits, dashes and dots) if non-empty — the name, never the credential |
| Unset behaviour | no pull secret is named, and the install sheet offers no step to create one |

kubemg stores only the name. With one set, the install sheet's first step
creates the Secret from credentials in the operator's own shell. Debug
containers do not use it: an ephemeral container pulls with the pod it joins.
See [Air-gapped installs](../install/air-gapped.md#a-mirror-that-requires-authentication).

### `debug_image`

| | |
|---|---|
| Meaning | The image an [ephemeral debug container](../clusters/terminals-and-logs.md#debugging-a-pod-with-no-shell) runs — `kubectl debug`'s trick for a pod whose own containers have no shell to exec into. |
| Environment default | `KUBEMG_DEBUG_IMAGE` (falls back to `busybox:1.36`) |
| Validation | none beyond trimming |
| Unlike `shell_image` | there is no matching enable switch — any grant that can already exec into a pod can already ask for a debug container, and the cluster's own RBAC decides whether the write lands |

### `audit_retention_days`

| | |
|---|---|
| Meaning | How many days a proxied call stays in the audit table before the background pruner removes it. |
| Environment default | `KUBEMG_AUDIT_RETENTION_DAYS` (falls back to `30`) |
| Range | 1–3650 |
| Unusable stored value | Read as unset (the environment default applies), never guessed at |
| Clear with | `0` |

The pruner re-reads this setting on every pass, so shortening retention
takes effect without a restart.

### `session_recording_retention_days`

| | |
|---|---|
| Meaning | How long a terminal session recording (the `.cast.gz` file plus its index row) is kept. |
| Default | The **audit retention window** — not an independent environment variable |
| Range | 1–3650 when set explicitly |
| Ceiling | **Clamped down to `audit_retention_days` on read**, not refused on write, so a value that was legal when saved never becomes an error because the audit window later shortened. |
| Clear with | `0` (falls back to following the audit window) |

A recording must not outlive the audit record that says the shell was opened.

### `audit_verbs`

| | |
|---|---|
| Meaning | The comma-separated set of verbs that reach the audit **table**. Narrows a busy fleet's trail, which is overwhelmingly `list`/`get` calls nobody reads back. |
| Environment default | none — unset means every verb is recorded |
| Validation | Each entry must be one of the suppressible verbs listed in [Audit trail](../audit/trail.md#selective-audit-audit_verbs); an unrecognised verb in a submitted list is refused |
| Empty submission | Means **"back to every verb"**, never "record nothing" — the floor below still records regardless of this setting |
| Applies to | The audit table only. The structured log and any [forwarder](../audit/trail.md) still carry every verb |

Three things this selection can never suppress, whatever verbs are chosen: a
refusal or error, any streaming call (`exec`/`attach`/`portforward`/`log
-f`), and kubemg's own `replay`/`recording-get`/`recording-delete` records.

### `record_exec_sessions`

| | |
|---|---|
| Meaning | Runtime switch for interactive session recording (asciinema casts of `exec`/`attach`). |
| Environment gate | Can only be **on** if the server was started with a recording directory (`KUBEMG_SESSION_RECORDING_DIR`); `recording_available` in the response reports whether that is true |
| Effect of turning off | Stops the *next* shell from being recorded; a shell already running keeps recording |
| Effect of turning on with no directory configured | No effect — a process with nowhere to write cannot be talked into recording by a database row |

### `record_manifest_diffs`

| | |
|---|---|
| Meaning | Stores the field-level diff of a manifest write on its `update` audit row. |
| Default | **off**, with no environment variable behind it |
| Why it defaults off | A manifest can carry values as sensitive as a Secret (an inlined token in a ConfigMap, a password in a Deployment's env), so recording diffs is retained data an operator opts into |

### `kubeconfig_max_ttl_hours`

| | |
|---|---|
| Meaning | The longest a generated kubeconfig may be asked to live, in hours. |
| Default | 24 hours; no environment variable |
| Absolute ceiling | 90 days. The setting moves the ceiling within that bound, never past it |
| Range | 1 hour to 2160 hours |
| Unusable stored value | Read as unset (the 24-hour default applies) |
| Clear with | `0` |
| Stored in | **Hours**, not days, so an install can go below a day (an eight-hour shift) as well as up to a quarter |

## Branding (a separate surface, on purpose)

The console's own identity — your organisation's name and mark, an environment
banner, and a footer notice — is stored alongside the settings above but is
**not** part of `GET|PUT /api/v1/settings`. It has its own pair of routes:

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/v1/branding` | **none** | What the console should draw. |
| `PUT /api/v1/branding` | admin | Writes it. |

Branding differs from a setting in three ways:

- **No environment default.** A value is either stored or absent.
- **It changes nothing the server does.** These five fields are only drawn.
- **The read is unauthenticated**, so an environment banner shows on the
  sign-in page, before anyone types a password. It carries no version, provider,
  address or cluster.

| Field | Bound | Notes |
|---|---|---|
| `organisation_name` | 60 characters | Drawn beside the `kubemg` lockup, never instead of it. |
| `organisation_mark` | 64 KB decoded | A base64 `data:` URI. **A URL is refused** (an air-gapped console cannot fetch it, and a remote image would beacon from the sign-in page). PNG, JPEG, GIF and WebP only; **SVG is refused** because it can carry script. |
| `banner_text` | 120 characters | Empty means no banner, which is the default. Whitespace runs are folded to one space — a pasted newline would push every page's content down. |
| `banner_tone` | `neutral`, `caution`, `critical` | A tone stored without text is not reported as a banner. |
| `footer_notice` | 160 characters | The classification or handling line, beside the release number in the footer. |

An omitted field is left alone and a field sent empty is cleared — the same
convention the settings routes use.

## Deployment posture (read-only, not a setting)

`GET /api/v1/settings/deployment` (admin only) reports facts about the running
process that no setting can change; they are fixed at boot:

- Whether HTTPS is enabled, and whether the certificate being served is
  self-signed, operator-supplied, or minted by kubemg itself.
- Whether the JWT signing key came from `JWT_SECRET` or was generated and
  stored in the database.
- Whether an explicit agent CA bundle (`KUBEMG_AGENT_CA_BUNDLE`) is set.
- Whether session recording is enabled, and whether the recording encryption
  key is configured.

Each fact comes back as a check with a `key`, `title`, `severity`
(`ok`/`warn`/`blocked`), `detail`, and a literal `fix` line naming the
environment variable or file to change. It is the same read the setup wizard's
preflight step shows, so you can find these facts again after onboarding.

## First-run setup routes

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/v1/setup/state` | none | Reports `{"required": bool}`: whether this install still needs first-run setup. Unauthenticated because the sign-in page renders before a session exists. A database failure reads as "not required". |
| `GET /api/v1/setup/preflight` | admin | Everything the wizard cannot fix through a form: `admin_password_pristine` (the seeded administrator still holds its original password), the deployment `checks` above, and the settings `warnings` below. |
| `POST /api/v1/setup/complete` | admin | Stamps setup as finished. **Refuses (409)** while the bootstrap administrator's password is unchanged. |

## The kubeconfig policy endpoint

`GET /api/v1/kubeconfig/policy` (any authenticated user) reports
`min_ttl_seconds`, `default_ttl_seconds` and `max_ttl_seconds` — the
resolved ceiling from `kubeconfig_max_ttl_hours` above. Anyone who can generate
a kubeconfig can read it, so the form never learns the ceiling by being refused.
The kubeconfig drawer offers a fixed ladder (1h through 90d) filtered by it.

## Warnings disclosed in the console

Warnings are computed from the **effective** settings and shown verbatim on the
General Settings page and in the setup wizard's preflight step.

- **A raised kubeconfig ceiling.** Whenever the effective
  `kubeconfig_max_ttl_hours` exceeds the 24-hour default, the console shows:

    > "Kubeconfigs may be issued for up to `{duration}`. Through an agent
    > tunnel that is safe to revoke — every call re-reads the caller's grant —
    > but a direct-mode kubeconfig carries a token minted on the cluster,
    > which keeps working until it expires however the grant changes."

    This is a **disclosure**, not a refusal: the ceiling is yours to choose, and
    the console states the consequence each time.

- **A loopback public URL:**

    > "The server URL is a loopback address. An agent running inside a
    > cluster resolves it to its own pod, so it will never reach kubemg —
    > set the address the cluster can reach."

- **A plain-HTTP public URL** (when the host is not loopback):

    > "The server URL is plain http. Agent traffic and kubectl exec both
    > need TLS in production."

See also [Production checklist](../install/production-checklist.md) and
[Environment variables](../install/environment.md) for the full list of
boot-time configuration this page's defaults are drawn from.
