# Building and testing

```bash
make verify
```

That is the gate. It runs, in order: `manifest-check`, `backend-vet`,
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

## The four gates that are not ordinary tests

**`make manifest-check`** diffs `deploy/kustomize/base/` against the copy
embedded in `backend/pkg/agentpkg/base/`. The manifests exist twice on purpose —
one copy for humans to read and apply, one the server renders install packages
from — and this target is what stops them drifting. Edit both or neither.

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

## What to do about a flaky or slow gate

Nothing in `verify` is allowed to be flaky, and a test that is
becomes a bug of its own. If a gate is slow enough to be skipped in practice, it
will be skipped in practice — raise it as an issue rather than working around
it locally.
