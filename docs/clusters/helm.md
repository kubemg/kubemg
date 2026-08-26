# Helm releases

Helm 3 has no API of its own and no in-cluster controller: a release is
simply a `Secret`, labelled `owner=helm`, holding the whole release object as
`base64(base64(gzip(JSON)))`. kubemg's Helm surface is not a new kind of
access — it is the ordinary secrets list, read through the same impersonated
tunnel every other read uses, with the payload decoded server-side because a
browser has no business gunzipping a release just to render a table.

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
response**, from any Helm route. `GET .../helm/releases/:name/values`
returns exactly what `helm get values` would: the `config` the operator
supplied, not the chart's own defaults merged in.

## Writing values

`PUT .../helm/releases/:name/values` writes the supplied values the way a
`helm upgrade` writes a new revision — it **appends** a new Secret
(`sh.helm.release.v1.<name>.v<n+1>`) rather than editing the current one in
place, because editing in place would rewrite `helm history` and break `helm
rollback`. The previous revision is marked `superseded` in both the label
Helm queries by and the payload Helm reads, once the new revision exists —
if that second write fails, the release is still correct (Helm reads the
highest revision regardless), but `helm history` would show two rows marked
`deployed`, and the response's warning says so.

!!! warning "kubemg renders nothing"
    Every values write carries this warning, verbatim, because it is the one
    limitation that matters most about this feature:

    > Saved as a new Helm revision. This records the values Helm will start
    > from — it does not re-render the chart, so nothing running changes
    > until the next `helm upgrade`.

    kubemg has no chart to template from. The new revision carries the
    **previous** revision's chart and manifest forward unchanged, and **the
    cluster keeps running exactly what it was running** the moment before the
    write. The next real `helm upgrade` is what actually applies the new
    values. `HelmValuesDrawer` shows this warning before the first keystroke,
    not after the save — and a client that bypasses the UI entirely and calls
    the API directly is still told, because the warning travels on the
    response.

## History and rollback

`GET .../helm/releases/:name/history` returns every stored revision, newest
first, read from the same decode path (`latestHelmSecret`/`helmRevisions`)
the release list uses — there is no second code path that could disagree
about which revision is current.

`POST .../helm/releases/:name/rollback` restores an earlier revision, and it
is **deliberately less than `helm rollback`** — this is by design, not an
oversight:

- Helm's own rollback restores a revision's values, chart **and** rendered
  manifest, then applies that manifest to the cluster. The applying is the
  whole point of it, and it is also the one thing kubemg cannot reimplement
  without rebuilding Helm's three-way merge and deletion pass against
  objects it does not own.
- What kubemg's rollback restores is the target revision's **`config` (its
  values) and nothing else**. The chart metadata and manifest are carried
  forward from the **current** revision, because that is what is actually
  running — recording the target's own chart/manifest instead would leave
  the *next* `helm upgrade` diffing against a state the cluster was never
  actually in.
- It is the exact same append `updateHelmReleaseValues` performs, with its
  values read out of history instead of off the wire — same impersonated
  write, same audit record.

Resolution rules, both enforced server-side:

- The requested revision is resolved against what the cluster's own history
  actually returned, **never turned directly into a Secret name** — a
  revision Helm has since pruned answers `404` (*"`<name>` has no revision
  `<n>` — Helm may have pruned it"*), not a guess at a name that does not
  exist.
- Rolling back to the **current** revision is refused with `409` (*"revision
  `<n>` is already the current one"*) — appending an identical copy is a
  write that changes nothing while hiding that it did nothing.

The caveat travels with both the read and the write: the surface offering
rollback states its limit before the click, the same way the values editor
does.

## Front end

Explore gives Helm a section of its own rather than folding it into
Workloads — a release is what *produced* several workloads, not a workload
itself. Its rows open the shared detail drawer on two panels,
`HelmValuesPanel` and `HelmHistoryPanel`, since a release has no manifest for
the ordinary object route to address.
