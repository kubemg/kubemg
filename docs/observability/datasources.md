# Datasources

kubemg's own live reads answer "what is this using *right now*" — the
Kubernetes Metrics API keeps about two minutes of history and nothing older.
Anything with a longer memory — a chart over an hour, a log line from a pod
that no longer exists — has to come from a series backend the cluster already
runs, or a central one it reports to. A **datasource** is where kubemg is told
that backend exists.

## Per cluster, per kind

A datasource is stored per cluster **and per kind** (`metrics` or `logs`),
one row each, in `observability_sources`. It is not a server-wide setting:
two clusters have two Prometheuses, and registering the second one must not
mean editing a global config file. A cluster has *the* metrics backend and
*the* logs backend — not a list of candidates — so registering a new one for
a kind replaces whatever was there.

## The two access shapes

=== "in-cluster"

    Reached down the cluster's own agent tunnel, by asking the cluster's API
    server to proxy to a Service. The request path is:

    ```
    /api/v1/namespaces/{namespace}/services/{scheme}:{name}:{port}/proxy{prefix}{path}
    ```

    Nothing has to be exposed outside the cluster — the Service can sit on a
    ClusterIP with no route from anywhere else — and the call is
    **impersonated and audited exactly like every other tunnel read**. This is
    the shape a kube-prometheus-stack or a VictoriaMetrics cluster install
    takes: the series live where the cluster put them.

    A credential on an in-cluster source is pointless and is flagged as such:
    the cluster's own API server makes the onward call, so there is nowhere
    for kubemg to attach an `Authorization` header. If the backend needs
    auth, it has to be reached in `direct` mode instead.

    In-cluster access needs a connected agent. Registering (or reading) one
    against a **direct-mode** cluster is refused with `409 Conflict`:

    > *"an in-cluster datasource is reached through the agent tunnel, which a
    > direct-mode cluster does not have — give its external address
    > instead"*

=== "direct"

    Dialled straight from the bastion at a stored URL. This is the shape a
    central Thanos or a hosted Mimir takes, where the series live outside the
    cluster they describe. A credential — bearer token or basic auth — is
    valid here and is applied as an `Authorization` header on the outgoing
    request. `insecure_skip_verify` is available for an internal certificate
    the bastion process does not trust; it is a per-source opt-in, never a
    default.

## Providers

| Kind | Provider | Default port | Default path prefix |
|---|---|---|---|
| metrics | VictoriaMetrics | `8428` (single-node, root); vmselect answers on `8481` under `/select/0/prometheus` | none for single-node |
| metrics | Prometheus | `9090` | none |
| metrics | Thanos | `9090` | none (point at the Querier, not a sidecar or the store gateway) |
| metrics | Mimir | `8080` | `/prometheus` |
| logs | VictoriaLogs | `9428` | none |
| logs | Loki | `3100` | none (point at the gateway or the query frontend, not an ingester) |

The four metrics providers all speak the Prometheus query API
(`/api/v1/query`, `/api/v1/query_range`), which is why they share one probe
and one query engine. VictoriaLogs speaks LogsQL and Loki speaks LogQL — two
unrelated languages, each with its own query builder and decoder (see
[Metrics and logs](metrics-and-logs.md)).

A path prefix is the single most common reason a correctly-addressed
datasource answers 404: vmselect serves the Prometheus API per tenant
(`/select/0/prometheus` for the default tenant), and Mimir's gateway serves
it under `/prometheus`. Get the prefix wrong and the address still answers —
just not on the path being asked.

## What a save checks

A save does not require the backend to already exist — an operator may be
configuring a datasource ahead of installing it — but every `PUT` runs a
**probe** anyway, and the verdict is stored, so nothing is quietly assumed to
work.

The probe is a real read of the provider's own API, not a port check —
something listening on the port is not proof it is the backend anyone
configured:

| Provider | Probed with |
|---|---|
| VictoriaMetrics, Prometheus, Thanos, Mimir | `GET /api/v1/query?query=1`, then `GET /api/v1/status/buildinfo` for the version |
| VictoriaLogs | `GET /select/logsql/query?limit=1&query=%2A` |
| Loki | `GET /loki/api/v1/labels`, then `GET /loki/api/v1/status/buildinfo` for the version |

A non-2xx answer is turned into the next thing to try rather than a bare
status code: `401`/`403` names the missing or wrong credential, `503`/`502`
says the backend is reachable but not serving, and **`404` is explained as a
path-prefix problem** — "answered, but not on `/api/v1/query?query=1`; check
the path prefix, and that this really is Prometheus."

## Testing a draft before saving

`POST /api/v1/clusters/:id/observability/sources/:kind/test` runs the same
probe against a body that has **not been saved** — the registration
wizard's whole reason for existing: an operator finds out an address is
wrong while still looking at the field holding it. Omitting the credential
in a test body probes the one already stored, so "check this again" works
without re-typing a token.

`POST .../sources/:kind/check` re-probes the **stored** datasource and
records the result, so the cluster page can say when it was last known good.

## Credential handling

A credential is treated exactly like a cluster's service account token:
**stored, never serialised.** The API never returns the value — only
`has_credential`, a boolean. Saving a source with the credential field
omitted keeps whatever is stored, so an operator can fix a port number
without re-typing a bearer token; sending it as an empty string clears it.

## Discovery

`GET /api/v1/clusters/:id/observability/discover` reads the cluster's own
Services (through the same tunnel, impersonation and audit trail as every
other read — discovery is not a privileged back door) and matches their
names and ports against a table of signatures: a Service named `vmselect`
answering on `8481` scores higher than one merely containing "prometheus" in
its name on a non-standard port. Every candidate carries its `score` and a
`reason`, so a low-confidence guess is visibly a guess rather than presented
as a fact.

Deliberately excluded: `node-exporter`, `kube-state-metrics`, `alertmanager`,
`pushgateway`, `vmagent`, `vminsert`, `vlinsert`, `promtail`, `grafana`,
`metrics-server`, `vmalert`, `ruler`, `compactor`, `distributor`, `ingester`,
`store-gateway`, and anything named `operator`, `operated`, `headless`,
`canary`, `agent`, `exporter`, or `memberlist`. These are scrape targets,
write endpoints, or infrastructure components — a node-exporter answers on
`/metrics` and would look alive while returning nothing anyone asked kubemg
for. Offering one as "the metrics backend" is worse than offering nothing.

A match is a **suggestion, never a configuration**: nothing is stored until
an operator picks a candidate and saves it.

## Who may read, who may write

Reading the registered datasources (`GET .../observability`, which returns
`sources`, `agent_attached`, `connection_mode` and `editable`) is open to
**anyone the cluster is granted to** — a developer has to know a series
backend exists before they can be shown a chart drawn from it. Writing
(`PUT`/`DELETE .../sources/:kind`) is **admin only**. This is the same split
[consoles](consoles.md) uses, for the same reason: a link, or a datasource
address, is not itself access to anything — the credential behind it never
leaves the server either way.

## The wizard's optional step

The registration wizard's fourth step is this same panel, reused verbatim,
and it is explicitly **optional**: a cluster is usable without a series
backend registered, and the step says so rather than blocking the wizard.
The live Metrics API meters still work with nothing configured here — what
is missing is history.

See also [Metrics and logs](metrics-and-logs.md) for how a chart or a log
search is built from a registered datasource, and [Linking other
consoles](consoles.md) for how a datasource's Grafana UID turns a chart into
an Explore link.
