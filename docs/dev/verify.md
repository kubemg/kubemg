# Building and testing

```bash
make verify
```

That is the gate. It runs, in order: `manifest-check`, `chart-test`, `backend-vet`,
`backend-test`, `backend-build`, `agent-vet`, `agent-test`, `agent-build`,
`frontend-lint`, `frontend-test`, `frontend-contrast`, `frontend-build` and
`docs-build`. Nothing is proposed for merge without it, and on Apple Silicon it
needs `DOCKER_DEFAULT_PLATFORM=linux/amd64` in front of it (see
[Local development](setup.md#apple-silicon)).

`docker-compose.ci.yml` exposes the same jobs as compose services, which is what
a CI runner uses:

```bash
docker compose -f docker-compose.ci.yml run --rm backend-test
docker compose -f docker-compose.ci.yml run --rm frontend-build
```

**This is not a local-only convenience.** `.github/workflows/pr-checks.yml`
runs every one of those twelve services as its own check, `make verify (<service>)`,
on every pull request into `master` and again on push — the exact command a
contributor runs locally, nothing duplicated into the workflow itself. A PR
that fails to compile, fails `go vet`, breaks a test, or fails lint shows a red
check with that service's name; a clean PR shows all twelve green. This runs
alongside, and independently of, the security-focused jobs in the same
workflow (gitleaks, the two Trivy scans, govulncheck, the documentation build)
— those answer "is this safe to merge", this answers "does it work".

## The five gates that are not ordinary tests

**`make manifest-check`** diffs `deploy/kustomize/base/` against the copy
embedded in `backend/pkg/agentpkg/base/`. The manifests exist twice on purpose —
one copy for humans to read and apply, one the server renders install packages
from — and this target is what stops them drifting. Edit both or neither.

**`make chart-test`** verifies the management plane's Helm chart
(`deploy/helm/kubemg/`) in the pinned `helm-unittest` image, by running
`hack/chart-check.sh`. It lints the chart against every value set in `ci/`,
renders each one twice and fails if the two renders differ, refuses any
template that calls `lookup` or a random or certificate-generating function,
and then runs the unit tests in `tests/`. The first three are one rule: Argo CD
and Flux render with `helm template`, where `lookup` answers nothing and a
generated value changes on every render — so a chart that used either would
install one database password or certificate and replace it on the next sync.
A refusal the chart makes (`fail` in `_helpers.tpl`) and every value that
reaches the pod belong in `tests/`, asserted with `failedTemplate`, `contains`
and `equal` — not checked by installing the chart.

**`make frontend-contrast`** reads the design tokens out of `frontend/src/index.css`
and measures every colour pairing the components actually build against WCAG. It
is a gate, not a report: a violation fails the build. Fix it by moving the
**token**, never by adding an exception in a component. Accent and danger glyphs
on the rail are measured at 3:1 rather than 4.5:1, because they are glyphs
rather than text.

**`make docs-build`** builds this manual with warnings as errors. A link into a
heading that has since been renamed is the failure mode a manual actually has,
so `mkdocs.yml` promotes it from informational to a warning and the strict build
turns it into a failure. See [Writing documentation](docs.md).

**`make agent-image-check` / `make image-check`** build the published image
matrix (amd64 + arm64) without producing output. They are not in `verify`
because they are slow; run them when you touch a Dockerfile.

## Where each kind of test lives

### Backend

`backend/pkg/api` and its neighbours, plain `go test`. Seventy-odd test files
already sit there, and they are the right home for anything deterministic: a
refusal, a narrowing, a status code, a payload shape, an ordering rule.

```bash
make backend-test
```

### Console

```bash
make frontend-test    # vitest, also inside make verify
```

A pure derivation goes in a `src/**/*.test.ts` beside its module — `insights.ts`
and `objectForm.ts` are the pattern. A component assertion goes in a `.test.tsx`
that asks for a DOM in its own docblock:

```ts
/**
 * @vitest-environment jsdom
 */
```

vitest runs in the `node` environment by default, per file, so a component test
that forgets the docblock fails on `document`.

### The agent

```bash
make agent-test
```

Its own module, its own tests, and the wire format it mirrors from the bastion.
Bumping `ProtocolVersion` on either side without the other is a breaking change
the handshake refuses — see [The agent module](agent.md).

## Choosing a verification level

Verification has three levels, and the cheapest one that can actually answer the
question is the right one.

1.  **`make verify`.** Always, for everything. Non-negotiable.

2.  **A test in the suite.** The default for anything deterministic. A refusal,
    a narrowing, a redirect, an element rendered or not rendered, a payload
    shape: these are assertions, and an assertion belongs in a test that runs
    forever rather than in a browser session run once. Prefer writing the test
    over asking somebody to look at it — the test is cheaper on its second run
    and every run after.

3.  **An end-to-end pass against a real cluster.** Only for what the first two
    genuinely cannot reach: the real tunnel, a cluster's own RBAC answering,
    the agent handshake and install, streaming (`exec`, `logs -f`,
    `port-forward`), TLS, and anything whose failure mode is wiring *between*
    processes rather than logic inside one. Also for a change to the stack
    itself — `docker-compose.yml`, the `Makefile`, the agent manifests.

A presentation refactor that adds no capability does not need level three.

An end-to-end pass costs real time, so give it a scope rather than a sweep:
the routes and pages touched, explicit acceptance criteria, and the failure
paths that need a real cluster — a scoped grant refused by the cluster's own
RBAC, a missing agent. Report **pass, fail or untested per criterion**, with the
exact reproduction for every failure. A broad regression sweep is the test
suite's job. A red end-to-end run is never checked off, and if the environment
genuinely cannot run the stack, say so rather than skipping the step silently.

### The end-to-end fixture

Most of what a level-three pass used to cost was building the same cluster
state by hand. `make e2e-up` builds it once and keeps it:

```bash
make e2e-up     # idempotent: against a live fixture it changes nothing
make e2e-down   # the deliberate reset; leaves the dev stack and minikube up
```

It runs on the host, not in a container, because it drives a local cluster:
it needs `docker`, `minikube`, `kubectl`, `helm`, `jq`, `curl`, `openssl` and
`git`. In order, it:

1.  starts the dev stack if `/health` does not answer, and checks that the
    bastion's certificate covers the host the agent will dial — an agent that
    cannot verify it fails with nothing but an x509 error;
2.  starts minikube if it is stopped;
3.  builds the agent from **this tree's** `agent/` and loads it into minikube,
    tagged `kubemg-agent:e2e-<tree hash>` so an unchanged tree reuses it and an
    edited one (`-dirty`) is always rebuilt;
4.  writes two runtime setting overrides — `public_url` (the address the agent
    dials) and `agent_image` — which `make e2e-down` clears again, and only if
    they still hold the fixture's values;
5.  registers two agent-mode clusters, applies the agent package to the first
    and waits for the tunnel;
6.  seeds the cluster and ends by printing a summary of all of it.

| What | Where |
| --- | --- |
| Cluster with the agent attached | `e2e-minikube`, kube context `minikube` |
| Agent-mode cluster with no agent | `e2e-detached` |
| Namespace-scoped `view` user | `e2e-viewer` / `e2e-viewer-pass`, on `e2e-apps` of `e2e-minikube` only |
| Deployment, 2 replicas | `e2e-apps/e2e-web`, `registry.k8s.io/pause` — runs, turns Ready, has no shell |
| Helm release | `e2e-apps/e2e-release`, from the local chart in `hack/e2e/chart` (nothing to pull) |
| CRD family with two kinds | `stable.e2emulti.example`: `Widget`, `Gadget` — its own sidebar section |
| CRD family with one kind | `things.e2esingle.example`: `Solo` — lands in *Other* |
| Traffic map shop | `e2e-apps`: Ingresses `shop` and `docs`, HTTPRoutes `shop` and `docs` on Gateway `edge`, VirtualService `shop` on Istio Gateway `public` + `mesh`, over Services `shop-api` (healthy), `shop-checkout` (a pod that never turns Ready), `shop-legacy` (matches no pods) and `e2e-payments/ledger` (outside the viewer's grant). The Ingress also names a port `shop-api` does not expose and a Service that does not exist; HTTPRoute `shop` carries a `RefNotPermitted` status. See `hack/e2e/manifests/traffic.yaml`. |
| Dependency map | `e2e-apps/shop-worker`: ConfigMap `shop-config` (read for keys), optional `shop-flags` (absent → warning), Secret `shop-db` (never created; the pod's `CreateContainerConfigError` is how the map learns it), ServiceAccount `shop-worker`, claim `shop-data` bound by minikube's default StorageClass. |
| Gateway API / Istio | Stand-in CRDs (`traffic-crds.yaml`, labelled `e2e.kubemg.io/fixture`) — applied only when no real ones are installed, and the only CRDs of those groups `e2e-down` removes. No controller runs: the fixture writes the HTTPRoutes' status itself. |

One agent namespace holds one cluster's agent. If `kubemg-system` on the
profile already runs another KubeMG cluster's agent, the fixture **refuses**
rather than detaching that cluster; `E2E_REPLACE_AGENT=1` takes the namespace
over deliberately.

Everything else is a variable with a default:

| Variable | Default |
| --- | --- |
| `E2E_ADMIN_USER` / `E2E_ADMIN_PASSWORD` | `admin` / `admin` |
| `E2E_PUBLIC_URL` | `https://host.docker.internal:8443` |
| `E2E_MINIKUBE_PROFILE` | `minikube` |
| `E2E_API` | `https://localhost:8443` |
| `E2E_AGENT_VERSION` | `0.0.0-e2e` (what the agent reports) |
| `E2E_STACK_TIMEOUT` / `E2E_ATTACH_TIMEOUT` | `300` / `180` seconds |

When the dev database has not finished first-run setup, the fixture says so:
the console opens on the setup wizard, and the API is unaffected.

## What to do about a flaky or slow gate

Nothing in `verify` is allowed to be flaky, and a test that is
becomes a bug of its own. If a gate is slow enough to be skipped in practice, it
will be skipped in practice — raise it as an issue rather than working around
it locally.
