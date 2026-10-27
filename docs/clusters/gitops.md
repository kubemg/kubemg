# GitOps sync state

The GitOps page (`/clusters/:id/gitops`) shows which objects in a cluster no longer match what Argo CD or Flux declares in Git. Each object that a GitOps tool manages also shows its own sync state in its drawer.

KubeMG **reads the answer from the GitOps tool and never from Git**. Argo CD has already rendered your repository, compared the result with the cluster and recorded the outcome for each object. Flux has recorded which revision it applied. KubeMG shows those answers. It does not clone repositories or render manifests, so it cannot report a difference between two renders as drift, and it needs no repository credentials.

## What is read

| Tool | Record | What it tells you |
|---|---|---|
| **Argo CD** | `Application` | Sync and health for the application, and for **each object it manages**; the source, the compared revision and whether self-heal is on |
| **Flux** (kustomize-controller) | `Kustomization` | Ready or failed, the applied revision and, when they differ, the last attempted one; the objects in its inventory |
| **Flux** (helm-controller) | `HelmRelease` | Ready or failed, the chart version and whether drift detection is on |

A tool that is not installed in the cluster is skipped. If the cluster refuses a list, KubeMG shows the cluster's own message above the table, and the other tools are still shown.

### Flux does not record drift per object

Flux puts a hand edit back at its next reconcile and records nothing about it. So the state of each object in a Kustomization is shown as **Unknown**, never as Synced. A Kustomization reads **Out of sync** only when its last apply failed or a newer revision has been attempted but not applied. helm-controller does not list the objects a release made at all.

## The page

The page leads with three counts: the records you can see, how many are **out of sync**, and how many are **degraded** (something they manage is broken or missing). The table below puts records that are out of sync first, then broken ones.

Open a record to see its source, the applied revision, when it was last compared and what happens to a hand edit:

- **Argo CD with self-heal on** puts a hand edit back. With self-heal off, the edit stays until the next sync.
- **A Flux Kustomization** always puts a hand edit back, unless it is suspended.
- **A Flux HelmRelease** puts it back only when drift detection is set to `enabled`.

Click an object in the record to open it in the cluster's resource drawer. An object of a kind the console has no list for is shown as text.

An Application that deploys to **another cluster** is labelled with its destination. Its objects belong to that cluster, so they cannot be opened from here.

## In the resource drawer

When an object's metadata says that Argo CD or Flux tracks it, its drawer shows a **GitOps** panel. The panel finds the tracking record from the object's own tracking label or annotation and shows this object's sync and health. If the record is not in this cluster (Argo CD often runs in a separate management cluster), the panel says so.

The panel is read when the drawer opens. It does not refresh on its own; use the drawer's Refresh.

## Comparing live and desired state

For an object that Argo CD reports **out of sync**, the panel offers **Show diff**. It opens the object's live state next to the state Argo CD would sync it to, as a table of changed fields (live on the left, desired on the right) and both manifests in full.

The desired state is **Argo CD's own**: KubeMG asks Argo CD's API for that one object, already normalized the way Argo CD's diff view shows it, with `ignoreDifferences` applied. What reads as a difference here is what Argo CD counts as one. Status, `managedFields`, `resourceVersion`, `uid`, `generation`, `creationTimestamp` and the `last-applied-configuration` annotation are removed from both sides. Each manifest is cut at 256 KiB, and the diff says so when it is.

It needs an [Argo CD datasource](../observability/datasources.md#gitops-argo-cd) on the cluster. Without one the button is absent and the panel says why. Sync state does not depend on it.

What is refused, and in what order:

1. **A Secret is never compared.** Argo CD masks Secret values, but KubeMG does not rely on that: a Secret's values leave only through the audited reveal.
2. **You must be able to read the object.** It is read as you first. If the cluster refuses, that is the answer and Argo CD is not asked.
3. **The application comes from the object.** It is the one the object's own tracking annotation names, found and narrowed exactly as the panel finds it.

Flux keeps no desired state KubeMG can read, so Flux objects have no diff.

### Last change through KubeMG

The diff also names the **last write to the object that went through KubeMG** since Argo CD last finished a sync: who made it, which verb (`create`, `update`, `patch` or `delete`) and when. Refused writes do not count. If the Application records no finished sync, the whole retained audit trail is searched.

This follows the audit trail's own rule. An administrator sees anyone's write; anyone else sees only their own, and is told that writes by others are shown to an administrator only.

KubeMG can only name writes it carried. A `kubectl` with another credential, a controller, an admission webhook or Argo CD itself writes without passing through KubeMG. When nothing is found, the sheet says that a change made outside KubeMG leaves no record here — it never says that nobody changed the object.

## Opening it in Argo CD

KubeMG's view is read-only and narrowed to your grant. Sync, Refresh and the full resource tree are in Argo CD's own console, so every Argo CD record links to **its own page there**: each row on the GitOps page, the record's sheet, the drawer's GitOps panel and the diff. The page header links to the console itself.

The links need the cluster's [Argo CD console](managing.md#linking-other-consoles). They are built from that address, never from the Argo CD datasource's: the datasource is the address KubeMG dials, often an in-cluster Service a browser cannot open. Without a console link the links are absent, not disabled.

A link opens `/applications/<namespace>/<name>` in a new tab, so an Application outside Argo CD's own namespace opens correctly. You sign in to Argo CD as yourself, and Argo CD's own RBAC decides what you see there. A record hidden from you here never produces a link. Flux has no console, so Flux records have none.

## Alarms on drift

An alarm rule with the trigger **GitOps drift** fires when something goes out of sync. See [Alarms](../audit/alarms.md#gitops-drift).

## Who sees what

Applications usually live in Argo CD's namespace and Kustomizations in `flux-system`, and a namespace-scoped grant covers neither. So KubeMG reads the records itself and narrows them to your grant before showing them:

- With access to the whole cluster, you see every record.
- With access to some namespaces, you see a record only when it manages something in one of them, or lives in or deploys to one of them. Inside it you see only the objects in your namespaces. The rest are counted ("3 objects are outside your grant") but not named. A record's error message is hidden when anything was cut, because it can name those objects.
- With view access, Secret entries are never listed — the cluster's view role cannot read Secrets, so their names are not shown here either. They are counted with the hidden objects.
- An Application that deploys to another cluster is never shown to a namespace-scoped grant.

KubeMG never shows the parts of a record that can hold secrets: Helm values, Argo CD parameters and plugin environment, Flux `postBuild` substitutions, and HelmRelease values. Credentials written into a repository URL are removed.

These reads appear in the audit trail as `kubemg:gitops-reader`. They use the same identity setup as KubeMG's alarm and event watchers, so they need no change to the agent's manifests.

## Requirements

- An agent-mode cluster. A direct-mode cluster is refused, like every live read.
- Argo CD (any version with `argoproj.io/v1alpha1` Applications) or Flux v2 (`kustomize.toolkit.fluxcd.io/v1` or `v1beta2`; `helm.toolkit.fluxcd.io/v2`, `v2beta2` or `v2beta1`) installed in the cluster.

## Not offered

- KubeMG does not offer Sync or Refresh buttons. They would be writes into Argo CD's API outside the tunnel, which the cluster's audit never sees. Use the [link into Argo CD](#opening-it-in-argo-cd).
