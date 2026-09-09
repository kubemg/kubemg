# Runtime settings

Most of kubemg's environment-supplied configuration can be overridden at
runtime from the Settings pages, without a redeploy. `GET /api/v1/settings`
and `PUT /api/v1/settings` (both admin-only) are the surface behind them.

## Resolution rule

Every setting below follows the same rule:

1. The **environment variable** supplies the boot-time default.
2. A **stored override**, written through `PUT /api/v1/settings`, takes
   effect at the next read and wins over the default.
3. **An empty override means "use the default"** — that is how a setting is
   cleared. Sending `""` (or, for a numeric setting, `0`) removes the
   override rather than storing an empty value.
4. If the database cannot be reached, the resolution falls back to the
   **boot-time environment value** rather than failing the request —
   rendering an install command with a possibly-stale address is better than
   not rendering one at all.

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

This is the outside view of the bastion, not its listen address — a
loopback or private value here means an agent inside a target cluster can
never dial back in.

### `agent_image`

| | |
|---|---|
| Meaning | The container image installed into a target cluster when it registers in agent mode. |
| Environment default | `KUBEMG_AGENT_IMAGE` (falls back to the build's own default, currently `ghcr.io/kubemg/kubemg-agent:0.9.0`) |
| Validation | none beyond trimming |

### `agent_namespace`

| | |
|---|---|
| Meaning | The namespace the agent is installed into on a target cluster. |
| Environment default | `KUBEMG_AGENT_NAMESPACE` (falls back to `kubemg-system`) |
| Validation | Must be a valid Kubernetes name (lowercase letters, digits, dashes; not leading/trailing dash) if non-empty |

### `audit_retention_days`

| | |
|---|---|
| Meaning | How many days a proxied call stays in the audit table before the background pruner removes it. |
| Environment default | `KUBEMG_AUDIT_RETENTION_DAYS` (falls back to `30`) |
| Range | 1–3650 |
| Unusable stored value | Read as unset (falls back to the environment default) — a retention window read wrong is a trail deleted, so the read side treats a corrupted or out-of-range value as absent rather than guessing |
| Clear with | `0` |

The pruner re-reads this setting on every pass, so shortening retention
takes effect without a restart.

### `session_recording_retention_days`

| | |
|---|---|
| Meaning | How long a terminal session recording (the `.cast.gz` file plus its index row) is kept. |
| Default | The **audit retention window** — not an independent environment variable |
| Range | 1–3650 when set explicitly |
| Ceiling | **Clamped down to `audit_retention_days` on read**, not refused on write. A stored value that was legal when saved must not turn into a validation error just because the audit window later shortened — see `clampRecordingRetention`. |
| Clear with | `0` (falls back to following the audit window) |

A recording is evidence *about* a line in the audit trail; letting it
outlive the record that says the shell was opened at all would leave
orphaned evidence.

### `audit_verbs`

| | |
|---|---|
| Meaning | The comma-separated set of verbs that reach the audit **table**. Narrows a busy fleet's trail, which is overwhelmingly `list`/`get` calls nobody reads back. |
| Environment default | none — unset means every verb is recorded |
| Validation | Each entry must be one of the suppressible verbs listed in [Audit trail](../audit/trail.md#selective-audit-audit_verbs); an unrecognised verb in a submitted list is refused |
| Empty submission | Means **"back to every verb"**, never "record nothing" — the floor below still records regardless of this setting |
| Applies to | `StoreAuditor` only, never the structured-log auditor — narrowing a queryable table is a storage decision; narrowing what a SIEM tails would be an audit decision |

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
| Default | **off**, and there is no environment variable behind it — unlike every other setting here, this one starts disabled on purpose |
| Why it defaults off | A manifest body can carry values as sensitive as a Secret's without being a Secret — an inlined token in a ConfigMap, a password in a Deployment's env — so recording diffs is a new class of retained data an operator opts into rather than one that quietly starts happening |

### `kubeconfig_max_ttl_hours`

| | |
|---|---|
| Meaning | The longest a generated kubeconfig may be asked to live, in hours. |
| Default | No environment variable — the build's own `k8s.DefaultMaxTTL` (24 hours) |
| Absolute ceiling | `k8s.MaxTTL` (90 days / a quarter) — this setting can move the ceiling *within* that bound, never past it |
| Range | 1 hour to `k8s.MaxTTL` in hours (2160) |
| Unusable stored value | Read as unset (falls back to the 24-hour default) — the same "wrong read reads as absent" rule retention uses, because a ceiling read wrong is either every request refused or a credential that lives longer than this build is willing to sign for |
| Clear with | `0` |
| Stored in | **Hours**, not days — the setting has to move in both directions (an install granting a quarter, and one refusing anything past an eight-hour shift, are the same kind of decision), and only hours can express the second |

## Branding (a separate surface, on purpose)

The console's own identity — your organisation's name and mark, an environment
banner, and a footer notice — is stored alongside the settings above but is
**not** part of `GET|PUT /api/v1/settings`. It has its own pair of routes:

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/v1/branding` | **none** | What the console should draw. |
| `PUT /api/v1/branding` | admin | Writes it. |

Three things make it a different kind of thing from a setting, and they are why
it is not folded into the shape above:

1. **It has no environment default.** There is nothing sensible for a boot flag
   to say about a customer's logo, so the resolution rule at the top of this page
   does not apply: a branding value is either stored or absent.
2. **It changes nothing the server does.** Every setting above is a knob on
   behaviour; these five are drawn and nothing more.
3. **The read is unauthenticated, and has to be.** An environment banner exists
   to be read *before* somebody types a password into a console — one that
   appeared only after sign-in would be warning people about a console they are
   already inside. That is a deliberately different judgement from
   `GET /api/v1/setup/state`, which answers one boolean because a stranger must
   learn nothing: everything branding serves was typed into a form by an
   administrator who could see the sign-in page while doing it. It carries no
   version, no provider, no address and no cluster.

| Field | Bound | Notes |
|---|---|---|
| `organisation_name` | 60 characters | Drawn beside the `kubemg` lockup, never instead of it. |
| `organisation_mark` | 64 KB decoded | A base64 `data:` URI. **A URL is refused** — an air-gapped console cannot fetch a remote image, and one that can turns its own sign-in page into a beacon for whoever hosts it. PNG, JPEG, GIF and WebP only: **SVG is refused** because it can carry script and this is rendered for people who have not signed in yet. |
| `banner_text` | 120 characters | Empty means no banner, which is the default. Whitespace runs are folded to one space — a pasted newline would push every page's content down. |
| `banner_tone` | `neutral`, `caution`, `critical` | The deck's semantic colours. Lime is deliberately not offered: it is the interactive accent, and a banner is not something you press. A tone stored without text is not reported as a banner. |
| `footer_notice` | 160 characters | The classification or handling line, beside the release number in the footer. |

An omitted field is left alone and a field sent empty is cleared — the same
convention the settings routes use.

## Deployment posture (read-only, not a setting)

`GET /api/v1/settings/deployment` (admin only) reports facts about *this*
running process that no setting can change at runtime — they are fixed at
boot from the environment and TLS material on disk:

- Whether HTTPS is enabled, and whether the certificate being served is
  self-signed, operator-supplied, or minted by kubemg itself.
- Whether the JWT signing key came from `JWT_SECRET` or was generated and
  stored in the database.
- Whether an explicit agent CA bundle (`KUBEMG_AGENT_CA_BUNDLE`) is set.
- Whether session recording is enabled, and whether the recording encryption
  key is configured.

Each fact comes back as a `setupCheck` — `key`, `title`, `severity`
(`ok`/`warn`/`blocked`), `detail`, and a literal `fix` line naming the
environment variable or file to change. This is the same read the first-run
setup wizard's preflight step (`GET /api/v1/setup/preflight`, admin only)
shows — an install that inherited its configuration, or that took the fast
path through setup, can still find these facts later, rather than only
seeing them once during onboarding.

## First-run setup routes

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/v1/setup/state` | none | Reports `{"required": bool}` — whether this install still needs first-run setup. Unauthenticated by necessity: the sign-in page has to render before a session exists. A database failure reads as "not required", the safe direction, since the wizard overrides the whole console. |
| `GET /api/v1/setup/preflight` | admin | Everything the wizard cannot fix through a form: `admin_password_pristine` (the seeded administrator still holds its original password), the deployment `checks` above, and the settings `warnings` below. |
| `POST /api/v1/setup/complete` | admin | Stamps setup as finished. **Refuses (409)** while the bootstrap administrator's password is unchanged — every other thing the wizard collects is a preference; this one is the difference between an install that has actually been set up and one that merely looks like it has. |

## The kubeconfig policy endpoint

`GET /api/v1/kubeconfig/policy` (any authenticated user) reports
`min_ttl_seconds`, `default_ttl_seconds` and `max_ttl_seconds` — the
resolved ceiling from `kubeconfig_max_ttl_hours` above. It is deliberately
readable by anyone who can generate a kubeconfig, not admin-only: a form
offering a choice must not have to discover the ceiling by being refused.
The frontend's kubeconfig drawer filters a fixed TTL ladder (1h through 90d)
against this response rather than offering a free-text field.

## Warnings disclosed in the console

`settingsWarnings` computes warnings from the **effective** settings, and
they are surfaced in two places verbatim: the General Settings page, and the
setup wizard's preflight step.

- **A raised kubeconfig ceiling.** Whenever the effective
  `kubeconfig_max_ttl_hours` exceeds the 24-hour default, the console shows:

    > "Kubeconfigs may be issued for up to `{duration}`. Through an agent
    > tunnel that is safe to revoke — every call re-reads the caller's grant —
    > but a direct-mode kubeconfig carries a token minted on the cluster,
    > which keeps working until it expires however the grant changes."

    This is a **disclosure**, not a refusal — raising the ceiling is a
    policy an administrator is allowed to choose, but the console states the
    consequence every time it takes effect, because the two connection modes
    differ on exactly the thing that matters about a long-lived credential.

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
