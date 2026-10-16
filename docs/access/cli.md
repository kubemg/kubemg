# The kubemg command line

`kubemg` signs a command line in through the console, and then keeps `kubectl` supplied with short-lived credentials for agent-mode clusters. Nothing long-lived is written to disk, and the second factor is asked for once per sign-in, in the browser.

It works the way `aws eks update-kubeconfig` does: the CLI writes an `exec` entry into your kubeconfig and steps aside. You keep using `kubectl`, `k9s`, `helm` and IDE plugins as usual; each of them asks `kubemg` for a credential when it needs one.

!!! note "Agent-mode clusters only"
    A cluster registered for direct API access is reached with a token the cluster itself mints. No CLI session governs it and kubemg cannot revoke it, so the CLI lists it and points to the console download instead.

## Installing it

Download the archive for your system from the [releases page](https://github.com/kubemg/kubemg/releases) — `kubemg_<version>_<os>_<arch>.tar.gz`, or `.zip` for Windows — check it against `SHA256SUMS`, and put the `kubemg` binary on your `PATH`:

```bash
tar -xzf kubemg_0.16.0_darwin_arm64.tar.gz
sudo install kubemg_0.16.0_darwin_arm64/kubemg /usr/local/bin/
kubemg version
```

It is one static binary with no dependencies, released with the server and versioned with it. It is Apache-2.0 licensed, like the agent: you can vendor it into your own tooling.

## Signing in

1. Run `kubemg login --server https://kubemg.example.com`. It prints a code like `BCDF-GHJK` and opens your browser at **/cli/activate** with the code filled in. On a machine with no browser, open the printed address yourself, on any device.
2. Sign in to the console if you are not already — with your second factor, if your account has one. You come back to the approval page afterwards.
3. Check what the page shows: the machine name, the system, **the address the request came from**, and how long the session will last. If the address is not where you are, the page says so.
4. **Approve sign-in.** The terminal finishes on its own within a few seconds.

!!! warning "Only approve a sign-in you started just now"
    Approving gives the command line that asked *your* access. Somebody who starts a sign-in and sends you the link gets your clusters if you approve it. When in doubt, **Deny** — the CLI is told no and nothing is signed in.

A browser session that did not pass a second factor cannot approve when your account holds one, or must hold one: sign out, sign in again with the factor, and approve from the new session.

A code waits ten minutes. One address can have at most ten sign-ins waiting at a time.

## Adding clusters to your kubeconfig

```bash
kubemg clusters                                   # what you can reach
kubemg update-kubeconfig --cluster edge-us        # add it, make it current
kubectl get pods
```

`update-kubeconfig` writes three entries — the cluster (the console's proxy address and, for a self-signed server, its CA), a user that runs `kubemg credential`, and a context — into the first file in `$KUBECONFIG`, or `~/.kube/config`. Everything else in the file, comments included, is left as it was, and the previous version is kept beside it as `config.bak`. Running it again replaces kubemg's entries rather than adding more. The names are the ones the console download uses (`<you>@<cluster>`), so the two never disagree.

| Flag | |
|---|---|
| `--cluster NAME\|ID` | Which cluster. Required |
| `--namespace NS` | The context's namespace. Defaults to the first one your grant covers; one outside it is refused |
| `--alias NAME` | Name the context something else |
| `--kubeconfig FILE` | Write to this file instead |
| `--no-set-context` | Add the context without making it current |
| `--static --ttl 8h` | Write a kubeconfig that carries its own token instead of running kubemg — the console download, for a tool that cannot run a credential plugin. It is registered like any download and refused under `cli_only` |
| `--server URL` | Needed only when you are signed in to more than one server (or set `KUBEMG_SERVER`) |

The entry kubectl runs looks like this, and needs no editing:

```yaml
users:
- name: dana@edge-us
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kubemg
      args: [credential, --server, 'https://kubemg.example.com', --cluster-id, "7"]
      interactiveMode: Never
      provideClusterInfo: false
```

`kubemg credential` keeps the current credential in your user cache directory and reuses it until a minute before it expires, so a loop of kubectl calls does not ask the server each time. It never prompts. When the session has ended it fails with the command to run, which is what kubectl shows you:

```text
kubemg: the CLI session has ended: this CLI session was revoked; run kubemg login — run: kubemg login --server https://kubemg.example.com
```

If a session is ended while kubectl still holds one of its credentials — from the console, by an administrator, by a blanket revoke — the next call is refused at once, but kubectl reports it in its own words: `error: You must be logged in to the server (the server has asked for the client to provide credentials)`. Run `kubemg clusters`: it says whether the session has ended, and `kubemg login` starts a new one.

With `KUBEMG_TOKEN`, the variable has to be in the environment kubectl runs in, since kubectl is what starts `kubemg credential`.

## A self-signed server

Pass the server's CA once, at sign-in: `kubemg login --server https://kubemg.internal --ca-file kubemg-ca.pem`. It is kept with the session and written into every kubeconfig entry, so neither kubemg nor kubectl skips verification. There is no flag that turns verification off.

## In CI: machine accounts

A pipeline has no browser. Give it a [machine account](machine-accounts.md) token in `KUBEMG_TOKEN` and name the cluster by its numeric id — a machine token is bound to one cluster:

```bash
export KUBEMG_TOKEN=kmgm_…
kubemg update-kubeconfig --server https://kubemg.example.com --cluster 7 [--ca-file ca.pem]
kubectl get deploy -n payments
```

With `KUBEMG_TOKEN` set, `kubemg credential` hands kubectl that token as it is and never needs a session.

## How long it lasts

A sign-in lasts **12 hours** by default — a working day, so one second factor a day. Administrators change it under **Admin → Settings → General → The kubemg command line** (`cli_session_max_hours`, 1–720 hours). It is never longer than the kubeconfig window (`kubeconfig_max_ttl_hours`): a CLI session cannot outlive what a downloaded file could.

Within a session, each credential kubectl receives lives **15 minutes** and is renewed by the CLI as needed. When the session ends, kubectl stops with a message telling you to run `kubemg login` again.

## Where it keeps things

| What | Where |
|---|---|
| The session (refresh token, server, CA) | `~/.config/kubemg/sessions/` on Linux, `~/Library/Application Support/kubemg/sessions/` on macOS, `%AppData%\kubemg\sessions\` on Windows — or `KUBEMG_CONFIG_DIR` |
| The current kubectl credentials | the user cache directory's `kubemg/credentials/` — or `KUBEMG_CACHE_DIR` |

Both are readable only by you. `kubemg logout` deletes them as well as ending the session on the server.

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
