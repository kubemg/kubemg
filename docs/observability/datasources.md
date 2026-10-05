# Datasources

A datasource tells kubemg where a cluster's metrics, logs or alerts backend lives, so the console can draw history. An administrator registers one per cluster and kind; everyone granted the cluster then sees the charts and log searches it feeds.

The Kubernetes Metrics API keeps about two minutes of history. Anything older, such as an hour-long chart or a log line from a pod that is gone, has to come from a backend the cluster already runs.

## Per cluster, per kind

A datasource is stored per cluster **and per kind**: `metrics`, `logs` or `alerts`. A cluster has one of each, not a list of candidates. Registering a new one for a kind replaces the old one.

## The two access shapes

=== "in-cluster"

    kubemg asks the cluster's API server to proxy to a Service, down the agent tunnel. Nothing has to be exposed outside the cluster, and the call is impersonated and audited like every other tunnel read. This is the usual shape for kube-prometheus-stack or a VictoriaMetrics cluster install.

    - A credential is pointless here and is flagged: the cluster's API server makes the onward call, so kubemg cannot attach an `Authorization` header. If the backend needs auth, use `direct`.
    - It needs a connected agent. Registering one on a **direct-mode** cluster is refused with `409 Conflict`: *"an in-cluster datasource is reached through the agent tunnel, which a direct-mode cluster does not have — give its external address instead"*.

=== "direct"

    kubemg dials a stored URL straight from the server. This is the shape for a central Thanos or hosted Mimir. A bearer token or basic-auth credential is valid and is sent as an `Authorization` header. `insecure_skip_verify` accepts an internal certificate the server does not trust. It is a per-source opt-in, never a default.

## Providers

| Kind | Provider | Default port | Default path prefix |
|---|---|---|---|
| metrics | VictoriaMetrics | `8428` (single-node, root); vmselect answers on `8481` under `/select/0/prometheus` | none for single-node |
| metrics | Prometheus | `9090` | none |
| metrics | Thanos | `9090` | none (point at the Querier, not a sidecar or the store gateway) |
| metrics | Mimir | `8080` | `/prometheus` |
| logs | VictoriaLogs | `9428` | none |
| logs | Loki | `3100` | none (point at the gateway or the query frontend, not an ingester) |
| alerts | Alertmanager | `9093` | none (the Service, not the headless `alertmanager-operated`) |

A wrong path prefix is the most common reason a correctly addressed datasource answers `404`. vmselect serves the Prometheus API per tenant (`/select/0/prometheus` for the default tenant), and Mimir's gateway serves it under `/prometheus`.

## What a save checks

A save does not require the backend to exist yet. Every save still runs a **probe**, a real read of the provider's own API rather than a port check, and stores the verdict.

| Provider | Probed with |
|---|---|
| VictoriaMetrics, Prometheus, Thanos, Mimir | `GET /api/v1/query?query=1`, then `GET /api/v1/status/buildinfo` for the version |
| VictoriaLogs | `GET /select/logsql/query?limit=1&query=%2A` |
| Loki | `GET /loki/api/v1/labels`, then `GET /loki/api/v1/status/buildinfo` for the version |
| Alertmanager | `GET /api/v2/status`, which carries the version too |

A failed probe says what to try next:

- `401`/`403` names the missing or wrong credential.
- `502`/`503` means the backend is reachable but not serving.
- `404` is explained as a path-prefix problem.

**Test** in the form runs the same probe against what is on screen, before you save. Leaving the credential blank probes the one already stored. **Check** re-probes the stored datasource and records when it was last known good.

## Credentials

The console and API never return the credential, only a `has_credential` flag. Saving with the field omitted keeps the stored value, so you can fix a port without re-typing a token. Sending an empty string clears it.

## Discovery

**Discover** reads the cluster's Services through the same tunnel, impersonation and audit as any other read, and suggests matches by name and port. Each candidate shows a `score` and a `reason`, so a weak guess looks like a guess. Nothing is stored until you pick a candidate and save.

??? info "What discovery leaves out"
    Scrape targets, write endpoints and infrastructure pieces are never offered: `node-exporter`, `kube-state-metrics`, `pushgateway`, `vmagent`, `vminsert`, `vlinsert`, `promtail`, `grafana`, `metrics-server`, `vmalert`, `ruler`, `compactor`, `distributor`, `ingester`, `store-gateway`, and anything named `operator`, `operated`, `headless`, `canary`, `agent`, `exporter` or `memberlist`. A node-exporter would look alive while returning nothing kubemg asks for. An Alertmanager is offered as the `alerts` candidate, not for metrics or logs.

## Alerts (Alertmanager)

The `alerts` kind is the cluster's Alertmanager. The [Alerts page and an object's Alerts panel](alerts.md) read it, and alarms cannot be created without it.

It has one extra setting, **rule labels**: the labels given to every alarm's `PrometheusRule` so the cluster's Prometheus loads it. They are the Prometheus resource's `spec.ruleSelector`. kube-prometheus-stack selects `release=<its release name>`.

- **Read from cluster** fills the field from the cluster's Prometheus resource. It says so when labels alone are not enough: a selector with match expressions, a Prometheus that loads rules only from its own namespace, or several Prometheus resources.
- Enter labels as `key=value, key=value`.
- `app.kubernetes.io/managed-by` and anything under `kubemg.io/` are kubemg's own and are refused.

An alarm with the wrong labels is a valid object that nothing evaluates, which is why the field is read from the cluster rather than guessed.

## Who may read, who may write

Anyone granted the cluster can read its registered datasources. Writing (add, edit, delete) is admin-only. Neither exposes the credential. The split is the same as for [other consoles](../clusters/managing.md).

## The wizard's optional step

The registration wizard's fourth step is this same panel and is optional. Without a datasource the live Metrics API meters still work. What is missing is history.

See [Metrics and logs](metrics-and-logs.md) for how charts and log searches are built from a datasource.
