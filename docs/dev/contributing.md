# Contributing

kubemg takes pull requests. What follows is the shape a change is expected to
arrive in — none of it is unusual, but two of the rules are load-bearing rather
than stylistic and are called out as such.

## Before you write anything

Read `roadmap.md` for what is open, and `roadmap-shipped.md` for what is
finished. A shipped entry says what was actually built **and what was
deliberately rejected**, so reading it first is what stops a pull request from
reimplementing something that was tried and removed on purpose.

Then read the relevant section of `ARCHITECTURE.md`. It carries the argument
behind each rule — the alternatives, and what must not be reintroduced. A rule
without its reason is easy to "fix" back into a bug, and that is the single most
common way a well-meant change breaks something.

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

Commit messages describe the change in full. Keep the description; it is the
attribution rules below that are narrow, not the message.

## Two rules that are not stylistic

**Verify before you propose.** `make verify` is the floor, and anything
deterministic gets a test in the suite rather than a note in the pull request
saying you checked it in a browser. A red end-to-end run is never checked off; if
the environment genuinely cannot run the stack, say so rather than skipping the
step silently.

**The manifests exist twice and the tokens have one home.** `make manifest-check`
and `make frontend-contrast` are gates for a reason. A contrast violation is
fixed by moving the design token, never by adding an exception in a component. A
manifest change is applied to both copies or neither.

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
