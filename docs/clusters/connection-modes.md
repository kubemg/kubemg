# Connection modes

Every cluster is registered in one of two modes: **agent** or **direct**. This page compares them so you can choose. The mode is fixed at registration; switching means registering the cluster again.

**Use agent mode unless you have a specific reason not to.** It is the only mode where a kubemg grant means something to the cluster itself, it needs no inbound network change, and it carries everything the console does.

## Agent mode

The cluster runs the open-source kubemg agent. The agent dials **out** to kubemg and holds a WebSocket tunnel open; kubemg never connects to the cluster.

- kubemg stores only a registration token, which authenticates the tunnel and nothing else.
- No inbound firewall rule and no exposed API server are needed.
- Health means "is the agent connected right now".
- A generated kubeconfig points at kubemg's own proxy and carries a kubemg-issued credential scoped to that one cluster. When kubemg's certificate is self-signed, its CA is embedded in the file.
- The cluster's own RBAC decides what a grant is worth: the agent's manifests bind `kubemg:view`, `kubemg:edit` and `kubemg:cluster-admin` to the built-in roles, and every call is made as the caller.

See [The agent](agent.md) for what gets installed.

## Direct mode

kubemg dials the cluster's API server itself, like any `kubectl` client.

- kubemg stores the API URL, an optional CA certificate and a **service account token**: a standing credential for the cluster, held in kubemg's database.
- It needs a network path from kubemg to the API server.
- Health is a live probe of that URL.
- A generated kubeconfig carries a short-lived token minted by the cluster itself and points straight at the API server.

!!! warning "Direct mode does not enforce your grants inside the cluster"
    kubemg mints tokens but creates **no RoleBinding**. A generated kubeconfig gets whatever the stored service account is already bound to, so a `view` grant does not make anything read-only inside the cluster. The cluster dashboard, the permissions page and the last wizard step all say which mode applies.

## Comparison

| | Agent | Direct |
|---|---|---|
| Cluster credential held by kubemg | None, only a registration token | API URL and service account token |
| Inbound port on the cluster | No | Usually yes |
| Who connects | The cluster, outbound | kubemg, inbound |
| Health check | Tunnel connected | Live probe of the API server |
| Kubeconfig points at | kubemg's proxy | The cluster's API server |
| Kubeconfig credential | kubemg-issued, scoped to the proxy route | Token from the cluster's TokenRequest API |
| Cluster RBAC applies to a grant | Yes | No, only kubemg's own permissions |
| Revoking a kubeconfig | Immediate | Not possible from kubemg; the token lives until it expires |
| Explore, metrics, in-console queries | Available | Refused: *this cluster is registered for direct API access; generate a kubeconfig instead* |
| `exec`, `attach`, `logs -f`, `port-forward`, browser shell, session recording | Available | Not available; use your own `kubectl` with the kubeconfig |
| Machine accounts (programmatic tokens) | Supported | Refused with `409` |
| In-cluster metrics/logs source | Supported | Refused with `409`; a `direct` source (a URL kubemg can dial) still works |
| Alarm event polling | Available | Not available |
| Network requirement | Outbound HTTPS from the cluster to kubemg's public URL | A route from kubemg to the API server |

In direct mode, everything kubemg can show you comes from the health probe (name, reachability, Kubernetes version). The rest is `kubectl` against the kubeconfig it issues.

??? info "Why agent mode exists"
    Direct mode was the first path: register a cluster you already have API access to without deploying anything. It remains useful for a quick look under kubemg's own authorization model. It is not a fit wherever a kubemg `view` grant must really be read-only inside the cluster, because it is not.

## Moving a cluster from direct mode to agent mode

There is no in-place switch. You register the same physical cluster again in agent mode, then remove the old registration. The two registrations are independent until you delete one.

1. **Register the cluster in agent mode** from the [wizard](registering.md), run the install command against the same cluster, and wait for the agent to attach. This creates a new cluster with a new ID.
2. **Re-create access grants** on the new cluster. Grants belong to a cluster ID and do not carry over. Re-point any [guardrail policies](../access/guardrails.md) scoped to the old one.
3. **Re-issue every kubeconfig.** Files generated for the direct-mode cluster keep working against the cluster until they expire, entirely outside kubemg's control. Only a file generated for the new cluster gets impersonation, instant revocation and kubemg's TLS.
4. **Re-check anything keyed on the cluster**: machine account grants, metrics and logs sources (an in-cluster source is now allowed), linked consoles, and saved audit filters or bookmarks naming the old cluster.
5. **Remove the old registration** from [Managing a cluster](managing.md#the-inventory-table) once traffic has moved.
