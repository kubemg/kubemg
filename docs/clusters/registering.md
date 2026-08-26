# Registering a cluster

Registration is a page, not a drawer: `/admin/clusters/new`, a five-step
wizard (`ClusterWizard.tsx`) rather than a modal form. It is admin-only. The
five steps are **Identity**, **Connection**, **Handshake**, **Observability**
and **Access** — the stepper lets you jump back to any completed step, but the
first two lock once the cluster record exists, because steps three through
five act on the real cluster rather than on a draft.

## Step 1 — Identity

| Field | Notes |
|---|---|
| Name | Required. Must be unique — a duplicate name is refused with a 409 ("cluster name already registered"). |
| Environment | One of `prod`, `staging`, `dev`. Drives the environment band shown on the fleet overview and the cluster's `EnvironmentTag`. |
| Description | Optional free text. |

Nothing is written to the server yet — this step only holds local form state.

## Step 2 — Connection

Pick **Agent-based** (recommended) or **Direct API access**. See
[Connection modes](connection-modes.md) for what each actually means. This is
also where the cluster record is **created** — submitting this step calls
`POST /api/v1/clusters` and everything after it acts on the returned cluster.

=== "Agent mode"

    No further fields on this step. The server mints a registration token
    (`bastion.NewAgentToken()`) and stores it as `Cluster.AgentToken`; no other
    cluster credential is stored. The response already carries the rendered
    install command, since the server renders it as part of creating the
    cluster.

=== "Direct mode"

    Three additional fields, all validated before the record is created:

    | Field | Validation |
    |---|---|
    | API server URL | Required, must parse as a URL (`api_url`, `binding:"omitempty,url"` doesn't apply here — direct mode requires it). |
    | CA certificate | Optional. PEM or base64-encoded PEM. Rejected at registration time (`k8s.DecodeCACert`) rather than later at kubeconfig generation, when a bad value would be far more confusing to debug. Leave empty for a publicly trusted API server certificate. |
    | Service account token | Required. Needs permission to create service accounts and request tokens (kubemg uses it to create the per-user service account and issue `TokenRequest`s later). |

    Missing `api_url` or `service_account_token` in direct mode is refused
    with a 400 ("api_url and service_account_token are required for a direct
    connection").

Once the cluster exists, steps 1 and 2 are locked — the mode-select cards
become disabled and the connection fields, if any, stop rendering.

### Equivalent REST call

```bash
curl -X POST https://your-kubemg/api/v1/clusters \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "name": "prod-eu-west-1",
    "environment": "prod",
    "connection_mode": "agent"
  }'
```

or, for direct mode:

```bash
curl -X POST https://your-kubemg/api/v1/clusters \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "name": "prod-eu-west-1",
    "environment": "prod",
    "connection_mode": "direct",
    "api_url": "https://prod-eu.example.com:6443",
    "ca_cert_data": "-----BEGIN CERTIFICATE-----\n...",
    "service_account_token": "eyJhbGciOi..."
  }'
```

See [reference/api.md](../reference/api.md) for the full cluster surface.

## Step 3 — Handshake

What this step shows depends on the mode:

- **Agent mode**: the one-line install command (`kubectl apply -f
  https://…/install/<token>/agent.yaml`), a Kustomize alternative, a
  downloadable YAML, and the raw manifest to review before applying it. The
  step polls `GET /api/v1/clusters/:id` every three seconds and stops the
  moment `agent_attached` becomes `true`. This poll is deliberately **not**
  gated on the tab being visible — unlike every other live read in the
  console — because being away from this screen while pasting a command into
  a terminal elsewhere is the expected state.
- **Direct mode**: a **Run check** button that calls
  `POST /api/v1/clusters/:id/check`, which actually probes the stored
  `api_url`.

You can continue past this step ("Skip for now") before the cluster connects
— the connection state is visible everywhere else in the console afterward,
not just here.

See [Deploying the agent](agent.md) for exactly what the install command
fetches and applies.

## Step 4 — Observability

The shared `DatasourcePanel`, reused verbatim from the cluster's own
Observability panel. Explicitly optional: a cluster is fully usable with no
metrics or logs source registered, and the step says so instead of blocking.
Wiring a source here is exactly the same PUT the cluster's own Observability
panel uses later — see [Observability datasources](../observability/datasources.md).

## Step 5 — Access

Grants the first permissions on the new cluster — the same operation the
permissions matrix performs, narrowed to one cluster. Pick:

- **Grant to**: a group (every member inherits the grant) or a single user.
- **Kubernetes role**: `view`, `edit`, or `cluster-admin`.
- **Namespaces**: comma-separated, or leave empty for every namespace the role
  allows.

Each grant calls `POST /api/v1/permissions/assign`; revoking one calls
`POST /api/v1/permissions/revoke`. Existing grants on this cluster are listed
below the form with a revoke action.

The step ends with a mode-aware disclosure:

- **Direct mode**: a warning that these grants govern kubemg's own
  authorization only — kubemg issues a token but creates no RoleBinding, so
  the grant decides what a kubeconfig *claims*, not what the cluster allows.
- **Agent mode**: a note that the grant decides which cluster and namespaces
  kubemg will carry someone to, while the cluster's own RBAC (reachable via
  its ClusterRoles once the agent is attached) decides what they may actually
  do.

## After registration

The cluster now appears in the fleet overview and the admin inventory
(`/admin/clusters`). Day-2 operations — health checks, the dashboard, deleting
a cluster, curating which CRDs Explore offers — are covered in
[Managing a cluster](managing.md). Access can also be granted later from
**Admin → Permissions**, or requested on demand through
[just-in-time elevation](../access/jit.md).
