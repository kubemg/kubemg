# Helm releases

Helm 3 has no API of its own and no in-cluster controller: a release is
simply a `Secret`, labelled `owner=helm`, holding the whole release object as
`base64(base64(gzip(JSON)))`. kubemg's Helm surface is not a new kind of
access — it is the ordinary secrets list, read through the same impersonated
tunnel every other read uses, with the payload decoded server-side because a
browser has no business gunzipping a release just to render a table.

Reading, deleting values and rendering a chart are all built on
`helm.sh/helm/v3` used as a **library** — kubemg does not reimplement
Helm's chart engine — but deliberately **not** on `helm.sh/helm/v3/pkg/kube`,
Helm's own Kubernetes client. Applying rendered objects goes down the same
impersonated, audited tunnel every other write uses instead, which is also
why the server binary pulls in neither `k8s.io/kubectl` nor Helm's OCI
stack.

## How releases are read

`GET .../resources/helm/releases` reads every Secret matching
`owner=helm` and decodes it, then **deduplicates to the highest revision per
release** — the one describing what is actually installed. Because reading a
release means reading a Secret, and the built-in `view` ClusterRole
deliberately excludes Secrets, **a `view` grant is refused here in the
cluster's own words** — that is the correct answer, not a gap to fix. An
`edit` or `cluster-admin` grant is required to see Helm releases at all in
agent mode.

## What is, and is not, returned

Only chart metadata and the operator-supplied **values** ever leave the
server:

```json
{
  "name": "payments-api",
  "namespace": "payments",
  "chart_name": "payments-api",
  "chart_version": "4.2.1",
  "app_version": "1.9.0",
  "revision": 7,
  "status": "deployed",
  "updated_at": "2026-08-01T12:00:00Z"
}
```

The release object also carries the chart's **rendered manifest** — for many
charts that manifest contains generated passwords — and it **never enters a
response**, from any Helm route, install and upgrade included. `GET
.../helm/releases/:name/values` returns exactly what `helm get values`
would: the `config` the operator supplied, not the chart's own defaults
merged in.

## Installing a chart

`POST .../resources/helm/releases` installs a chart from a registered [chart
repository](helm-repositories.md), resolving the version against the
**stored catalogue** rather than fetching blind — which is what keeps an
install from being steered at an arbitrary URL. A published digest is
verified against the downloaded archive. `409` if a release of that name
already exists ("upgrade it instead"); `POST
.../helm/releases/:name/upgrade` for that case.

Rendering is Helm's own engine: `.Values` (chart defaults deep-merged with
subcharts', the operator's values on top, `global` threaded through,
subcharts switched by `condition`/`tags`), `.Release`, `tpl`, and sprig.
`.Capabilities.KubeVersion` and `.APIVersions` come from a real discovery
pass against the **target cluster**, through the tunnel — the same pass
supplies the Kind→plural mapping every write in this feature is built from,
because `Ingress` is `ingresses`, `Endpoints` is `endpoints`, and no single
pluralisation rule gets both right.

Write order follows Helm's own: CRDs from `crds/` first, then pre-install
hooks, then the release proper in Helm's own install order (Namespace →
ServiceAccount/Secret/ConfigMap/PV/PVC/RBAC → workloads), then post-install
hooks. Each rendered object is its own `get` then `create`-or-`update`, one
at a time, decided by the **target cluster's own RBAC** and earning its own
audit record — a forty-object chart is forty audit rows, and installing
nothing is a code path that emits none.

CRDs written from `crds/` are deliberately **not recorded on the release**
— the same reason `helm uninstall` leaves CRDs behind.

The manifest editor's deny list on creatable kinds (four RBAC kinds, Node)
does **not** apply to an install: cert-manager, ingress-nginx and every
operator worth installing ship a ServiceAccount, a ClusterRole and a
binding, and refusing those would leave the install button able to install
nothing anyone actually wants.

A namespace-scoped grant is checked **before the first write**: a chart
containing a cluster-scoped object, or one that installs into a namespace
outside the grant, is refused with the object named, and nothing is
written. Finding out on object nineteen is the failure this pre-flight
exists to prevent.

!!! warning "A failed install is still recorded, and does not roll back"
    If a write partway through fails, the run stops there. The release is
    recorded as `failed`, and the response names exactly which objects were
    written and which were not — kubemg does not delete the ones that
    succeeded on the caller's behalf. There is no `--atomic` rollback: that
    would mean removing objects nobody asked kubemg to remove. The release
    is written even though it failed, because the objects it created are
    real, and a release nobody recorded would be a set of orphans with no
    name attached.

## Upgrading a chart

`POST .../resources/helm/releases/:name/upgrade` re-renders against a new
version or new values and applies the difference with a **three-way
merge**: original is what the previous revision rendered, modified is this
render, live is what the cluster currently holds. A built-in kind gets a
strategic merge, so a sidecar a mutating webhook injected survives the
upgrade and an allocated `clusterIP` is not sent back as the chart's
template value; a custom resource gets a JSON merge patch, Helm's own
fallback for kinds with no defined merge key. A field the chart stopped
rendering is removed; a field nothing in the chart ever wrote is left
alone. The write carries the object's `resourceVersion`, so it stays
conditional and a concurrent change becomes `409` rather than being
silently overwritten, and it is a full `PUT` rather than a `PATCH` — the
tunnel carries one content type.

An object the previous revision wrote and this one no longer renders is
deleted **last, in reverse order, and never fatally** — a delete that fails
does not fail the upgrade.

## Writing values only

`PUT .../resources/helm/releases/:name/values` writes the supplied values
the way `helm upgrade --reuse-values` does, and — this is the change that
matters most about this feature — it now **renders and applies** them, the
same way [Upgrading a chart](#upgrading-a-chart) does: read the chart back
off the release itself, render it against the new values, and three-way
merge the result onto the cluster.

This needs no repository reachable and no repository configured at all,
because Helm stores the **whole chart** on the release — a release
installed from someone's laptop two years ago, from a chart kubemg has
never heard of, can still be re-rendered here.

!!! note "The one case that still can't be rendered"
    A release whose Secret was written by something that stripped the
    chart out of the stored object cannot be re-rendered — there is nothing
    to render. That case keeps the old append-only behaviour: the values
    write appends a new revision (`sh.helm.release.v1.<name>.v<n+1>`)
    carrying the **previous** revision's chart and manifest forward
    unchanged, and the cluster keeps running exactly what it was running
    before the write. The response's `helmValuesWarning` names this reason
    explicitly whenever it applies — it no longer appears on every write.

## History and rollback

`GET .../helm/releases/:name/history` returns every stored revision, newest
first, read from the same decode path (`latestHelmSecret`/`helmRevisions`)
the release list uses — there is no second code path that could disagree
about which revision is current.

`POST .../helm/releases/:name/rollback` is now `helm rollback`: it applies
the target revision's **stored manifest**, three-way merged against the
current revision's manifest as the original, the same as an upgrade.
Helm does not re-render on rollback either — the manifest is a fact about
what was actually running at that revision, and re-rendering would produce
today's answer to `.Capabilities` and `lookup`, not that revision's. The new
revision this creates records the target's chart, config **and** manifest
together, which is correct because the cluster is actually being moved to
that state.

Resolution rules, all enforced server-side:

- The requested revision is resolved against what the cluster's own history
  actually returned, **never turned directly into a Secret name** — a
  revision Helm has since pruned answers `404` (*"`<name>` has no revision
  `<n>` — Helm may have pruned it"*), not a guess at a name that does not
  exist.
- Rolling back to the **current** revision is refused with `409` (*"revision
  `<n>` is already the current one"*) — appending an identical copy is a
  write that changes nothing while hiding that it did nothing.
- A revision that recorded no stored manifest — an old revision written
  before this feature existed, or one written through the values-only
  fallback above — answers `409` naming the reason: there is nothing to
  three-way merge against.

## Honest limits

!!! warning "What kubemg's Helm engine does not do"
    - **Hooks are applied in weight order but not waited on.** kubemg does
      not hold an HTTP request open until a pre-install `Job` finishes, so a
      chart whose resources depend on a hook completing may briefly be in a
      state `helm install` would not show you. `hook-delete-policy` is not
      honoured, and `test` hooks are never run. Any response for a chart
      that declares hooks carries `hook_notice` saying so.
    - **OCI registries are not read.** A chart repository is `http(s)` only
      — see [Chart repositories](helm-repositories.md).

## Front end

Explore gives Helm a section of its own rather than folding it into
Workloads — a release is what *produced* several workloads, not a workload
itself. Its rows open the shared detail drawer on two panels,
`HelmValuesPanel` and `HelmHistoryPanel`, since a release has no manifest for
the ordinary object route to address.
