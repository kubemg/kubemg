# Managing a cluster

Day-2 operations on a registered cluster live in two places: the admin
inventory table at `/admin/clusters`, and the per-cluster dashboard at
`/clusters/:id/dashboard`.

## The inventory table

`/admin/clusters` (`ClusterManagement.tsx`), admin-only, lists every
registered cluster: name, rail chip, environment, link state, API server
(hidden below `md` width), status, Kubernetes version (hidden below `md`), and a
row of actions. A filter box narrows by name. From here:

- **Register cluster** opens the wizard at `/admin/clusters/new` — see
  [Registering a cluster](registering.md).
- **Run check** (per row) calls `POST /api/v1/clusters/:id/check`.
- **Edit** (per row) opens the labels sheet — see below.
- **Remove** calls `DELETE /api/v1/clusters/:id` after a confirmation:
  *"Remove `<name>`? Kubeconfigs already issued keep working until they
  expire."* — deleting the registration does not revoke kubeconfigs already
  handed out; in agent mode those still route through the proxy, which
  re-reads the grant on every call, so removing the cluster record itself
  is what actually stops them (there is nothing left to route to). Deleting a
  cluster does not uninstall its agent — see
  [Deploying the agent → Uninstalling](agent.md#uninstalling) for that.

## Editing a cluster's labels

`PATCH /api/v1/clusters/:id` (admin only) edits three fields and no others:

| Field | What it is |
|---|---|
| `short_name` | The chip the rail draws for this cluster. Up to four characters, folded to upper-case letters and digits — `eu-west-1` is stored as `EUWE`. Sending it **empty** clears it, which returns the cluster to the abbreviation the console derives from its name. |
| `environment` | `prod`, `staging` or `dev`. Drives the tag in the fleet list, the dot on the rail chip and the tint on the tree's edge. |
| `description` | Free text: what runs here, or who owns it. |

An omitted field is left alone; a field sent empty is cleared.

**There is deliberately no route that edits a connection.** An API URL, a CA
or a stored token is the cluster's identity as far as every kubeconfig, grant and
audit record already pointing at that row is concerned, so changing one in place
would silently re-aim all of them. Registering a different cluster is a
registration, and it should look like one — register the new cluster, move the
grants, and remove the old record.

The labels were frozen too until now, which was the actual defect: an operator
who mistyped an environment at registration had to delete the cluster — and every
grant on it — to correct a coloured dot.

### Why the chip is chosen rather than derived

The rail's chip used to be derived from the name: initials across a separated
name (`minikube-direct-e2e` → `MDE`), or the first three characters of a single
word (`LocalKube` → `LOC`). That works at three clusters and collapses at eleven,
where `prod-eu-west-1` and `prod-eu-west-2` both reduce to `PEW` and the rail
becomes a row of guesses. A rail exists to be built into muscle memory, and an
ambiguous abbreviation cannot be.

So the chip is now asked for at registration and editable afterwards. It is
**not unique** — two clusters sharing a chip is a mistake for an operator to see
and fix, and a uniqueness constraint would refuse a registration over a label.
The inventory table gives the chip a column of its own precisely so a collision
is visible in a line.

A cluster with no stored chip still gets the derivation, so a fleet registered
before this field existed looks exactly as it did.

## Health check

`POST /api/v1/clusters/:id/check` is mode-aware:

- **Agent mode**: the check does not touch the network at all — it asks the
  tunnel registry whether this cluster's agent is currently connected
  (`s.tunnels.Connected(cluster.ID)`). There is nothing for kubemg to dial;
  the whole point of agent mode is that kubemg has no route to the cluster.
  An unconnected cluster reports either *"no agent has connected from this
  cluster yet"* (never seen an agent) or *"the in-cluster agent is not
  connected"* (has connected before, isn't now).
- **Direct mode**: a real reachability probe against the stored `api_url`,
  which also refreshes the recorded Kubernetes version.

Either way the result is persisted (`UpdateClusterHealth`) and reflected in
`status`/`status_message`/`kubernetes_version`/`last_checked_at` on every
subsequent read of the cluster.

## The cluster dashboard

`/clusters/:id/dashboard` (`ClusterSummary.tsx`) renders one of two bodies
depending on the caller's coarse role (`user.role === 'admin'` — a super
admin counts as an administrator here too). The shell around both — the
header actions (Pods, Request access, Run check for admins only, Generate
kubeconfig), the kubeconfig sheet, the JIT request modal — is identical,
because those are things either kind of caller came to do.

### The administrator's body

The cluster **as an installation**: name, environment, status, when it was
last checked, the path traffic actually takes (cluster → kubemg → you, drawn
as connected nodes rather than prose), API server / Kubernetes version /
agent version or "direct API access" / when it was registered, live node
**capacity** (agent mode only — a live sample, not a series, so it renders as
meters rather than a chart), the [observability datasource
panel](../observability/datasources.md), [other consoles](../observability/consoles.md),
**CRD visibility curation** (agent mode only, see below), history charts
(cluster CPU/memory) and a ranked comparison table (CPU/memory by namespace,
restarts, not-ready containers, CPU throttling) once a datasource is wired
up, and a mode-aware closing panel explaining exactly what "how this cluster
is reached" means for it — including, in agent mode, links into the
cluster's own RBAC (`/clusters/:id/explore/clusterroles`) and its workload
security posture (`/clusters/:id/security`); in direct mode, a reminder that
a grant here decides what kubemg shows, not what the cluster allows.

Every one of those facts is something an administrator can act on — which is
exactly why none of it is shown to a developer.

### The developer's body

A slim identity card (Kubernetes version, their role, their granted
namespaces, whether calls are proxied), four summary cards — Deployments,
StatefulSets, DaemonSets, Pods — a **needs attention** list, and CPU/memory
history. Every number on this body is derived from the same resource-list
insight logic Explore's own pilot header uses (see
[Exploring resources](explore.md#the-pilot-header)), so a count here and a
count one click away in Explore cannot disagree; each card links to the list
it summarises rather than duplicating a table of its own.

## Node capacity

`/clusters/:id/capacity` (`NodeCapacity.tsx`) is its own address rather than a
tab on the dashboard, because it answers a different question than the
Capacity panel above: not "how much is this node using" but "what has the
scheduler already promised away". It shows, per node, three numbers against
the same allocatable denominator — reserved (requests), used (live, needs
metrics-server), and the ceiling if every container spent its limit — plus
pod-slot capacity, and lists pods the scheduler could not place with the
cluster's own reason. Live usage is the one column that can be missing; the
rest of the page stays whole without it.

## Curating which CRDs the Explore sidebar offers

Deriving Explore's custom-resource sections from a cluster's own CRD list is
what lets it browse an operator nobody at kubemg has heard of — and its cost
is that a cluster running two or three operators can declare a hundred kinds,
most of them one operator talking to itself (a lock object, an internal
revision, a generated certificate request). `CrdVisibilityPanel` on the
dashboard (agent mode only) is where an administrator says which of a
cluster's CRDs are actually worth showing.

- **What is stored is the hidden set** (`cluster_crd_visibility`, keyed
  `plural.group`), never the shown one — so an install that has never opened
  this panel behaves exactly as it always did, and installing a new operator
  tomorrow puts its kinds in the sidebar rather than silently hiding them
  until somebody notices and opens the panel.
- **`GET /api/v1/clusters/:id/crd-visibility`** is readable by **anyone the
  cluster is granted to** — a developer has to be able to tell "this cluster
  doesn't run Istio" from "somebody took Istio off the list" — while **`PUT`
  is admin-only** and replaces the whole set (bounded at 500 hidden entries;
  each entry validated against the `plural.group` pattern, which is also what
  keeps a fixed inventory key like `pods` out of a set that only ever
  describes CRDs).
- **This is curation, not access control.** A hidden kind disappears from the
  navigation and nothing else — the custom-resource read, the manifest
  editor and every object route still address it exactly as `kubectl` would,
  and what may actually be read is still the cluster's own RBAC to decide.
  Treating a hidden entry as a permission would claim an access control that
  a single `kubectl get` disproves.
- A store failure while reading the curation is treated as "nothing is
  hidden" — a blip that briefly re-shows a kind someone tidied away is far
  better than one that empties a developer's whole sidebar.
- Saving drops the cluster's read-cache scope and calls the inventory
  provider's `refresh()`, so the sidebar reflects a curation immediately
  rather than after the next page load.

## Linking other consoles

A cluster can declare where its Grafana and its Argo CD live, read by anyone
the cluster is granted to and written only by an admin. This is covered in
[Other consoles](../observability/consoles.md) — it is always a link, never an
embed and never a proxy.
