# The kubemg command line

`kubemg` signs a command line in through the console, and then keeps `kubectl` supplied with short-lived credentials for agent-mode clusters. Nothing long-lived is written to disk, and the second factor is asked for once per sign-in, in the browser.

It works the way `aws eks update-kubeconfig` does: the CLI writes an `exec` entry into your kubeconfig and steps aside. You keep using `kubectl`, `k9s`, `helm` and IDE plugins as usual; each of them asks `kubemg` for a credential when it needs one.

!!! note "Agent-mode clusters only"
    A cluster registered for direct API access is reached with a token the cluster itself mints. No CLI session governs it and kubemg cannot revoke it, so the CLI lists it and points to the console download instead.

## Signing in

1. Run `kubemg login --server https://kubemg.example.com`. It prints a code like `BCDF-GHJK` and opens your browser at **/cli/activate** with the code filled in. On a machine with no browser, open the printed address yourself, on any device.
2. Sign in to the console if you are not already — with your second factor, if your account has one. You come back to the approval page afterwards.
3. Check what the page shows: the machine name, the system, **the address the request came from**, and how long the session will last. If the address is not where you are, the page says so.
4. **Approve sign-in.** The terminal finishes on its own within a few seconds.

!!! warning "Only approve a sign-in you started just now"
    Approving gives the command line that asked *your* access. Somebody who starts a sign-in and sends you the link gets your clusters if you approve it. When in doubt, **Deny** — the CLI is told no and nothing is signed in.

A browser session that did not pass a second factor cannot approve when your account holds one, or must hold one: sign out, sign in again with the factor, and approve from the new session.

A code waits ten minutes. One address can have at most ten sign-ins waiting at a time.

## How long it lasts

A sign-in lasts **12 hours** by default — a working day, so one second factor a day. Administrators change it under **Admin → Settings → General → The kubemg command line** (`cli_session_max_hours`, 1–720 hours). It is never longer than the kubeconfig window (`kubeconfig_max_ttl_hours`): a CLI session cannot outlive what a downloaded file could.

Within a session, each credential kubectl receives lives **15 minutes** and is renewed by the CLI as needed. When the session ends, kubectl stops with a message telling you to run `kubemg login` again.

## Ending a session

Signed-in command lines are listed under **My credentials → CLI sessions** (yours) and **Admin → Credentials** (everyone's), with the machine, the address, when it signed in and when it was last used.

- **End** stops the session at once: every credential it handed out is refused at its next call, not when it expires.
- `kubemg logout` ends the session from the command line.
- **Revoke all of mine**, and changing your password with *revoke issued credentials* ticked, end every CLI session along with the kubeconfigs.
- Disabling an account stops its sessions at the next call.

Ending your own session never needs an administrator. Ending somebody else's always does; for anyone else, another person's session reads as not found.

## Withholding kubeconfig files

**Admin → Settings → General → Agent-mode kubeconfig files** (`kubeconfig_issuance`):

| Value | Effect |
|---|---|
| Allowed (`allowed`, default) | The console hands out kubeconfig files as before, alongside the CLI. |
| CLI only (`cli_only`) | No agent-mode kubeconfig file is handed out — not from the console, not with `kubemg update-kubeconfig --static`. The kubeconfig sheet shows the two CLI commands instead. |

`cli_only` does not touch direct-mode clusters: their credential is a token the cluster mints, which no CLI session governs, so the console download stays available for them. The browser shell and machine accounts are unaffected.

Together with a required second factor, `cli_only` is what lets you say that kubectl access to an agent-mode cluster always met the factor within the last day and leaves nothing long-lived on a laptop.

## In the audit trail

| Verb | When |
|---|---|
| `cli-login` | A sign-in was approved. The machine, system, CLI version and source address are in the record's path |
| `cli-session-revoke` | A session was ended — from the console, by `kubemg logout`, or by a blanket revoke. An administrator ending somebody else's is recorded with the holder as the impersonated user |

Every `kubectl` call made with a CLI credential is recorded like any other proxied call, under your name.

## Routes

| Route | Auth | Purpose |
|---|---|---|
| `POST /api/v1/auth/cli/device` | none | Start a sign-in (RFC 8628 device authorization) |
| `POST /api/v1/auth/cli/token` | device code | Poll for the result |
| `GET /api/v1/auth/cli/device/:code`, `POST .../approve`, `POST .../deny` | session | The approval page |
| `GET /api/v1/auth/cli/clusters`, `POST /api/v1/auth/cli/credential`, `POST /api/v1/auth/cli/logout` | CLI session | What the CLI calls |
| `GET /api/v1/cli-sessions`, `POST /api/v1/cli-sessions/:id/revoke` | session | The sessions in the credential register |
