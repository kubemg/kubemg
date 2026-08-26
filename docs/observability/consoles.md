# Linking other consoles

A cluster's metrics and logs draw from a fixed catalogue
([Metrics and logs](metrics-and-logs.md)), and the moment a question outgrows
it, the answer usually lives in a Grafana somebody already runs, or in
whatever deployed half the workloads Explore lists. `cluster_consoles`
stores where those other applications are — one row per cluster **per
kind**, the same shape [datasources](datasources.md) use and for the same
reason: a cluster has *the* Grafana that answers for it, not a list of
candidates.

## Kinds

| Kind | What it links to |
|---|---|
| `grafana` | The dashboards behind kubemg's own charts |
| `argocd` | What deployed the workloads Explore lists |
| `registry` | Whatever already scans this cluster's image registry for CVEs |

`registry` exists because the workload security posture view is explicit
that kubemg does not become a second vulnerability scanner: no registry
credential, no CVE feed. A finding that names a container image links out to
the registered scanner instead of guessing — the same link-only, no-credential
shape as the other two.

## A link, never an embed, never a proxy

kubemg holds no session for Grafana or Argo CD, sends them nothing, and
learns nothing back beyond the address itself. Two things were rejected for
a reason worth keeping in mind before either is reconsidered:

- **An iframe** would inherit the other console's origin and its session —
  the embedding page ends up carrying somebody's Grafana session inside it.
- **Proxying** the whole application down the agent tunnel would mean
  carrying a second application's routing, assets and websockets inside a
  transport built for the Kubernetes API.

So what is stored is an address and nothing else. The operator follows the
link and authenticates to Grafana or Argo CD as themselves.

## URL normalisation

`NormalizeConsoleURL` requires an absolute `http://` or `https://` address
with a host, and:

- **Refuses userinfo outright** rather than stripping it. A URL carrying a
  username and password *is* a credential, this table stores none, and
  silently dropping the half that makes it work would leave a link that
  fails with nothing to explain why.
- **Refuses a query string or a fragment** — a console needs its base
  address only; kubemg appends whatever query it builds.
- Trims a trailing slash so every caller appends to the same normalised
  form.

The console's `Ref` (an optional identifier — an Argo CD project label, for
example) is validated separately (`NormalizeConsoleRef`): trimmed, length
bounded, and refused if it contains `/`, `?` or `#`, because it names a
thing rather than a path.

## Who may read, who may write

Reading `GET /api/v1/clusters/:id/observability/consoles` is open to
**anyone the cluster is granted to**, for the same reason a datasource's
existence is not privileged information: a developer cannot be told to go
look at a dashboard in a place they are not allowed to know exists.
Registering or deleting a console (`PUT`/`DELETE .../consoles/:kind`) is
**admin only**. Nothing here is a credential and nothing here is access — a
link is safe to show as widely as the cluster itself is.

## The Grafana Explore link

Reading a metrics or logs query response also returns `grafana_explore`, a
ready-to-open link into Grafana's own Explore view over the identical query
and window kubemg just ran — when it can be built.

This link is built **server-side**, and that is not an implementation
detail. A metrics or logs query in kubemg is never the caller's own — the
browser names a chart from the fixed catalogue and the server writes the
PromQL or LogsQL/LogQL around the caller's scope (see [Metrics and
logs](metrics-and-logs.md)). If the browser assembled its own Grafana
Explore URL, it would be the browser writing a query — exactly what that
whole design exists to prevent. So `grafanaExploreFor` builds the link out
of the same expression the server already ran, using the documented
`panes` + `schemaVersion` Explore URL format.

Building it needs two things to exist:

1. A `grafana` console registered for the cluster.
2. The queried datasource's **uid in that Grafana**, stored on the
   datasource row itself as `grafana_datasource` (not on the console row) —
   because it identifies which of the two datasources one Grafana holds
   (metrics or logs) this query belongs to.

**No uid means no link.** A PromQL expression handed to a Grafana Explore
pane pointed at whatever it defaults to is an error message, not a chart —
so the field is left empty rather than offering a link that opens wrong.

## The Argo CD application link

`argoApplicationHref(base, name)` is built in the **browser**, unlike the
Grafana link — because it carries no query, only a path:
`{base}/applications/{name}`, URL-encoded. The application name comes off
the `argocd.argoproj.io/instance` label a workload already carries (Argo CD
writes it itself), so a workload row or drawer that has the label can link
straight to its owning application.

## The datasource's own UI

Some providers serve a browser-reachable query UI of their own — vmui for
VictoriaMetrics and VictoriaLogs, `/graph` for Prometheus and Thanos. `GET
.../observability/consoles` also returns `datasource_uis`, one entry per
registered source that has one, **derived** from the datasource's own stored
address rather than a separate field — storing the same fact twice would let
the two disagree the moment somebody moves a Prometheus.

This is offered **only for a `direct` source**. An in-cluster datasource has
no browser-reachable address by construction: it is read by asking the
cluster's API server to proxy to a Service under the caller's own
impersonated identity, down the tunnel — not a URL anything outside the
cluster can open. Offering a link there would be offering a link that
cannot work from an operator's laptop, which is the exact failure this
feature exists to avoid.
