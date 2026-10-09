# Managing a cluster

Day-2 operations on a registered cluster live in two places: the admin inventory at `/admin/clusters` and each cluster's dashboard at `/clusters/:id/dashboard`. This page covers both, plus node capacity, Explore sidebar curation and linking other consoles.

## The inventory table

`/admin/clusters` (admins only) lists every cluster: name, rail chip, environment, link state, API server and Kubernetes version (both hidden on narrow screens), status, and row actions. A filter box narrows by name, and `?agent=behind` shows clusters whose agent is older than the newest one in the fleet (the fleet page's *agents behind* figure opens it that way).

- **Register cluster** opens the [wizard](registering.md).
- **Run check** probes the cluster (see [Health check](#health-check)).
- **Edit** opens the labels sheet.
- **Remove** asks *"Remove `<name>`? Kubeconfigs already issued keep working until they expire."* In agent mode those kubeconfigs route through kubemg, so removing the record leaves them nothing to route to. Removing a cluster does not uninstall its agent; see [Uninstalling](agent.md#uninstalling).

## Editing a cluster's labels

Admins can edit three fields and no others. An omitted field is left alone; a field sent empty is cleared.

| Field | What it is |
|---|---|
| `short_name` | The rail chip, up to four characters, upper-case letters and digits (`eu-west-1` becomes `EUWE`). Empty returns to the abbreviation derived from the name. |
| `environment` | `prod`, `staging` or `dev`. Drives the tag in the fleet list, the dot on the rail chip and the tint on the tree's edge. |
| `description` | Free text: what runs here, or who owns it. |

**The connection cannot be edited.** An API URL, CA or stored token is the cluster's identity for every kubeconfig, grant and audit record that points at it, so changing one in place would silently re-aim all of them. To change it, register the new cluster, move the grants and remove the old record.

The chip is not unique. Two clusters sharing one is something for you to see and fix, and the inventory table gives the chip its own column so a collision shows at a glance. A cluster with no chip keeps the derived abbreviation.

## Health check

**Run check** depends on the mode:

- **Agent mode**: asks whether the agent is connected right now. It touches no network. An unconnected cluster reports *"no agent has connected from this cluster yet"* (never seen) or *"the in-cluster agent is not connected"* (seen before, not now).
- **Direct mode**: probes the stored API URL and refreshes the recorded Kubernetes version.

The result is saved and shown everywhere the cluster's status appears.

## The cluster dashboard

`/clusters/:id/dashboard` shows one of two bodies depending on the caller's role: administrators (super admins included) get the installation view, everyone else the developer view. The header actions (Pods, Request access, Generate kubeconfig, and Run check for admins only) are the same for both.

### The administrator's body

The cluster as an installation:

- Name, environment, status, last check, and the path traffic takes (cluster → kubemg → you).
- API server, Kubernetes version, agent version (or "direct API access") and registration date.
- **Usage** (agent mode only): cluster CPU and memory as two dials, with each node's reading beside them, busiest first. It is a live sample, not a series, and the dials change tone at 75% and 90%.
- History charts and a ranked comparison table (CPU and memory by namespace, restarts, not-ready containers, CPU throttling) once a datasource is wired up.
- An **Integrations** card with one tile each for [observability datasources](../observability/datasources.md), [other consoles](#linking-other-consoles) and **Explore sidebar** curation (agent mode only). A dashed tile is not connected yet.
- A closing panel on how the cluster is reached. In agent mode it links to the cluster's RBAC and workload security posture; in direct mode it reminds you that a grant decides what kubemg shows, not what the cluster allows.

### The developer's body

An identity card (Kubernetes version, your role, your namespaces, whether calls are proxied), four summary cards (Deployments, StatefulSets, DaemonSets, Pods), a **needs attention** list, and CPU and memory history. The numbers come from the same logic as Explore's pilot header ([Browsing resources](explore.md#the-pilot-header)), so a count here matches the one a click away, and each card links to the list it summarises.

!!! info "Screenshot pending — `cluster-dashboard.png`"
    A cluster dashboard: the connection chain as the page's masthead, with capacity below it read live from the cluster's Metrics API.

## Node capacity

`/clusters/:id/capacity` (reached from the dashboard's Usage **Capacity** button) answers "what has the scheduler already promised away" rather than "what is in use". Per node it shows three figures against the same allocatable total: reserved (requests), used (live; needs metrics-server) and the ceiling if every container spent its limit. It also shows pod-slot capacity and lists pods the scheduler could not place, with the cluster's own reason. Only live usage can be missing; the rest of the page works without it.

## Curating which CRDs the Explore sidebar offers

A cluster with a few operators can declare a hundred CRDs, most of them internal bookkeeping. Administrators choose which are worth showing from the **Explore sidebar** tile in the dashboard's Integrations card (agent mode only). The tile lists the cluster's API groups with how many kinds of each are shown; **Choose** opens the editor, with a switch per kind, a switch per API group, a filter, and *All* / *Shown* / *Hidden* views. Past 24 kinds the groups start folded.

- **Only the hidden set is stored.** A cluster nobody curated behaves as before, and a newly installed operator appears in the sidebar rather than being hidden.
- **Anyone granted the cluster can read the curation**, so a developer can tell "this cluster doesn't run Istio" from "someone hid it". Only admins can change it. A save replaces the whole set (at most 500 hidden entries, each written `plural.group`).
- **This is curation, not access control.** A hidden kind leaves the navigation and nothing else: you can still reach it with `kubectl`, and the cluster's own RBAC decides what may be read.
- If the curation cannot be read, nothing is hidden, so a blip never empties a developer's sidebar.
- Saving updates the sidebar immediately.

## Linking other consoles

A cluster can record where its Grafana, Argo CD and image-scanner live, so the console can link out to them. Anyone granted the cluster can read these links; only admins can add or remove them. One link per kind per cluster.

| Kind | What it links to |
|---|---|
| `grafana` | The dashboards behind kubemg's own charts |
| `argocd` | What deployed the workloads Explore lists |
| `registry` | Whatever already scans this cluster's image registry for CVEs |

The `registry` link exists because kubemg does not become a second vulnerability scanner (no registry credential, no CVE feed). A security finding that names an image links to your scanner instead.

**This is always a link.** kubemg holds no session for these tools, sends them nothing and learns nothing back; you sign in to Grafana or Argo CD as yourself. Nothing here is a credential or grants access.

??? info "Why not an embed or a proxy"
    An iframe would carry the other console's origin and session inside kubemg's page. Proxying a whole application through the agent tunnel would put a second application's routing, assets and websockets on a transport built for the Kubernetes API.

### Address rules

The address must be an absolute `http://` or `https://` URL with a host. It is refused if it contains:

- a username or password (that would be a credential, and quietly dropping it would leave a link that fails without explanation);
- a query string or fragment (the base address is all a console needs).

A trailing slash is trimmed. The optional reference (for example an Argo CD project label) is trimmed, length-limited, and refused if it contains `/`, `?` or `#`.

### Grafana Explore link

A metrics or logs query result can include a ready-to-open link to Grafana's Explore view over the same query and time window. kubemg builds it on the server, because the query itself is always written by the server, never by your browser (see [Metrics and logs](../observability/metrics-and-logs.md)).

It needs both:

1. A `grafana` link registered for the cluster.
2. The datasource's **uid in that Grafana**, stored on the datasource itself, because one Grafana holds both a metrics and a logs datasource.

**No uid means no link**: a query sent to the wrong Explore pane is an error message, not a chart.

### Argo CD application link

A workload carrying Argo CD's own `argocd.argoproj.io/instance` label links straight to its application at `{base}/applications/{name}`.

### The datasource's own UI

Some providers serve a query UI of their own (vmui for VictoriaMetrics and VictoriaLogs, `/graph` for Prometheus and Thanos). The console offers a link to it for each registered source that has one, derived from the datasource's stored address. This applies **only to `direct` sources**: an in-cluster datasource is reached through the cluster's API server and has no address an operator's browser could open.
