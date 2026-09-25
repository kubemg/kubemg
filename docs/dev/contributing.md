# Contributing

kubemg takes pull requests. This page describes how a change is expected to
arrive, and the standards it is reviewed against. None of it is unusual. The
rules under [The standards a change is held to](#the-standards-a-change-is-held-to)
exist for concrete reasons, and the page gives the reason with each one.

## Before you write anything

**Look for it first.** Search the
[issues](https://github.com/kubemg/kubemg/issues) and
[pull requests](https://github.com/kubemg/kubemg/pulls), open and closed. A
closed one often explains why something was built the way it was, or why it
was turned down.

**Open an issue for anything bigger than a fix.** Describe the problem before
the solution, say how anyone will know it is done, and say what it must *not*
turn into. The feature-request template asks for exactly those. Agreeing on the
shape before the code is cheaper for both sides than reviewing a finished branch
that went a different way. Issues labelled
[`good first issue`](https://github.com/kubemg/kubemg/labels/good%20first%20issue)
are scoped to be picked up without that step.

**Read the page for the area you are changing.** The [Internals](architecture.md)
pages carry each subsystem's shape and the rules it keeps. Where a rule looks
odd, the page says why, and a rule changed without its reason tends to come
back as a bug. If a rule is in your way, say so in the issue rather than
working around it in the diff.

## Branch and pull request

Never commit to `master`. One branch per task, off `master`:

```bash
git switch -c feature/short-description
# …
make verify
git push -u origin feature/short-description
gh pr create
```

The pull request body carries a summary and a test plan: what you changed, and
how you proved it. If a change needed an end-to-end pass against a real cluster,
say what you ran and what it reported — see
[Choosing a verification level](verify.md#choosing-a-verification-level).

Commit messages describe the change in full: what changed and why, in the
imperative (`Refuse a scoped grant a cluster-wide list`, not `fixed bug`). The
[attribution rules](#authorship-and-attribution) below are narrow. They do not
make the message short.

## The standards a change is held to

### Verification

**`make verify` is the floor, for every change.** It runs the backend, agent and
console tests, the linters, both builds, the manifest and contrast gates, and a
strict docs build.

**A deterministic behaviour gets a test.** That covers a refusal, a narrowing, a
payload shape, and an element drawn or not drawn. The test belongs in the suite,
not in a pull-request note saying you checked it in a browser. A test runs on
every change after yours. A browser check runs once.

**An end-to-end pass is for what a test cannot reach:** the real tunnel, a real
cluster's RBAC answering, streaming, TLS, and the agent install. When a change
needs one, the pull request says what was run and what it reported. A failed run
is never reported as passing. If your environment cannot run the stack, say that
instead of skipping it. See [Choosing a verification level](verify.md#choosing-a-verification-level).

### Security behaviour

kubemg is a security product, so these are review criteria, not style:

- **Refuse, and say why.** A request kubemg will not carry out gets a status
  code and a sentence naming the reason. It is never degraded quietly into
  something smaller, and never answered with an empty success.
- **Never report something that did not happen.** A revoke that could not land,
  a write that was refused and a partial run are each reported as what they
  were. That applies to the response and to the audit trail.
- **Fail closed on what decides access, and open only on what does not.** When
  the store cannot be read, a check that grants access refuses. Something purely
  presentational may fall back.
- **No credential in a response, a log line, the audit trail or a URL.** A
  secret field is write-only. A token on a query string is stripped before
  anything records it.
- **Authorization is the cluster's, scope is kubemg's.** Whether a role may do a
  thing is left to the target cluster's RBAC through impersonation. Namespace
  scope is enforced by kubemg, because Kubernetes cannot express it. Do not add
  a kubemg-side copy of the first, and do not weaken the second.
- **Someone else's record answers 404, not 403.** A 403 would confirm that the
  record exists.

### Code

- **Match the code around it.** Use its naming, its idiom and its comment
  density. A comment explains *why*. The code already says what.
- **No new pattern without the issue agreeing to it.** That means a new
  dependency, a new persistence mechanism or a second way to do something the
  codebase already does one way. The agent module in particular depends on
  `gorilla/websocket` and nothing else, and that is deliberate.
- **Upgrades are part of the change:**
    - A schema change is applied by `AutoMigrate`, with a numbered, idempotent
      reference file in `backend/migrations/`.
    - A manifest change goes to both copies and is called out, because existing
      agent installs must re-apply it.
    - Bumping the tunnel's `ProtocolVersion` breaks every attached agent, so it
      needs an explicit reason.
    - Anything an operator has to do on upgrade goes in the
      [upgrade notes](../install/upgrading.md#upgrade-notes-by-release).

### The console's visual language

- **Colours come from the tokens in `src/index.css`.** Never hard-code a hex
  value or add a one-off colour.
- **Every text tone clears 4.5:1 on every surface, in both themes.**
  `make frontend-contrast` enforces this. Fix a violation by moving the token,
  never with an exception in a component.
- **Lime is the only interactive accent.** Sage, amber and rust mean state.
  Text on lime is always ink, never white.
- **Nothing animates** except the one indicator for a stream that is genuinely
  open.

See [Console](frontend.md) for the rest.

### The manifests exist twice

The agent's manifests live in `deploy/kustomize/base/` for people and in
`backend/pkg/agentpkg/base/` for the install package the server renders.
`make manifest-check` fails if they differ. Change both or neither.

### Authorship and attribution

Commits are authored by the person submitting them, under their own name and
email. A commit or a pull request carries no attribution to a tool. That means:

- no `Co-Authored-By` line for a coding assistant
- no "Generated with …" line and no bot sign-off
- no link to, or id of, an assistant session

Once a trailer like that is pushed, the only way to remove it is to rewrite
published history. Use whatever tools you like to write the change; the record
names the person responsible for it.

## Licence of a contribution

The repository is split by directory: **AGPL-3.0** for the server and console,
**Apache-2.0** for `agent/` and `deploy/kustomize/`. A contribution lands under
the licence of the directory it touches, and moving code across that boundary
changes its licence — which makes it a question for review rather than a
refactor. `NOTICE` is the authority.

## Documentation is part of the change

A change that adds a setting, a route, a page or a refusal is not finished until
the manual says so. Which half it belongs in, and the house style, are in
[Writing documentation](docs.md). `make docs-build` runs inside `make verify`, so
a broken link fails the same gate the code does.

## Reporting a security issue

Do not open a public issue for a vulnerability. Report it privately to the
maintainer instead. The [Security model](../introduction/security-model.md) is
the honest short version of what kubemg protects and what it does not, including
the direct-mode limitation — worth reading before reporting, so a known and
documented gap is not filed as a new one.
