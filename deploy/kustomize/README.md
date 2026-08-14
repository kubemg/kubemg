# KubeMG agent deployment

`base/` is the Kustomize package for the KubeMG Dumb Agent: a ServiceAccount, a
narrow impersonation ClusterRole, the group bindings that make a KubeMG grant
mean something inside the cluster, a Secret holding the registration token, and
a single resource-capped Deployment.

## Normal installation

You do not fill this in by hand. Register the cluster in KubeMG
(**Clusters → Add cluster → Agent-based**) and the wizard hands you a one-line
install command. The bastion renders this package with that cluster's
registration token baked in and serves it at:

| Endpoint | Purpose |
| --- | --- |
| `GET /install/<token>/agent.yaml` | flattened manifest for `kubectl apply -f` |
| `GET /install/<token>/kustomize.tar.gz` | this package, rendered, for `kubectl apply -k` |
| `GET /api/v1/clusters/<id>/kustomize` | the same package as JSON, for the UI (admin JWT) |

`kubectl apply -k` cannot fetch a plain HTTPS URL — Kustomize only accepts local
paths and Git repository specs as remote targets — so the kustomize route is
consumed by extracting the archive first:

```
curl -sfL https://kubemg.example.com/install/<token>/kustomize.tar.gz | tar -xz
kubectl apply -k kubemg-agent
```

## GitOps installation

To manage the agent from your own repository, copy `base/` and substitute the
four placeholders yourself, or point an overlay at it:

| Placeholder | Value |
| --- | --- |
| `__AGENT_NAMESPACE__` | namespace to install into, normally `kubemg-system` |
| `__BASTION_URL__` | your KubeMG server's public URL |
| `__CLUSTER_TOKEN__` | the registration token from the wizard |
| `__AGENT_IMAGE__` | agent image, normally `ghcr.io/kubemg/kubemg-agent:<version>` |

Keep `__CLUSTER_TOKEN__` out of the repository — use your existing secret
management for the `kubemg-agent` Secret and drop `secret.yaml` from
`resources:`.

## Source of truth

The bastion embeds its own copy of `base/` at `backend/pkg/agentpkg/base/` so
the manifests ship inside the server binary. `make manifest-check` diffs the two
directories and fails the build if they drift, so any edit here has to be made
in both places.

## Licence

Apache-2.0 — see [`../LICENSE`](../LICENSE). These manifests and the agent they
install are the permissive half of this repository; the server and console are
AGPL-3.0.
