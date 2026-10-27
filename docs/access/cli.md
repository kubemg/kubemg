# The kubemg command line

`kubemg` signs a command line in through the console, and then keeps `kubectl` supplied with short-lived credentials for agent-mode clusters. Nothing long-lived is written to disk, and the second factor is asked for once per sign-in, in the browser.

It works the way `aws eks update-kubeconfig` does: the CLI writes an `exec` entry into your kubeconfig and steps aside. You keep using `kubectl`, `k9s`, `helm` and IDE plugins as usual; each of them asks `kubemg` for a credential when it needs one.

!!! note "Agent-mode clusters only"
    A cluster registered for direct API access is reached with a token the cluster itself mints. No CLI session governs it and kubemg cannot revoke it, so the CLI lists it and points to the console download instead.

## Installing it

**From your kubemg server.** The server's image carries the binary for every platform, so the people who use it can get it from the server they already reach — no internet access needed. The console offers the one for your system, with a `curl` line for a terminal, in two places:

- **My credentials → Use the command line** (the **You** group in the sidebar).
- The kubeconfig sheet of any agent-mode cluster, under **Or skip the file: use the kubemg command line** — with that cluster's name already filled into the commands.

```bash
curl -fLo kubemg https://kubemg.example.com/downloads/cli/linux/amd64 && chmod +x kubemg
sudo mv kubemg /usr/local/bin/
```

`GET /downloads/cli` lists what the server carries with each file's SHA-256; `GET /downloads/cli/{linux,darwin,windows}/{amd64,arm64}` serves one. Neither needs a session — the binary is open source, and whoever needs it has not signed in yet. A server built from source without the binaries (the development stack) says so and points at the releases page.

**From the releases page.** Download the archive for your system from the [releases page](https://github.com/kubemg/kubemg/releases) — `kubemg_<version>_<os>_<arch>.tar.gz`, or `.zip` for Windows — check it against `SHA256SUMS`, and put the `kubemg` binary on your `PATH`:

```bash
tar -xzf kubemg_0.17.0_darwin_arm64.tar.gz
sudo install kubemg_0.17.0_darwin_arm64/kubemg /usr/local/bin/
kubemg version
```

It is one static binary with no dependencies, released with the server and versioned with it. It is Apache-2.0 licensed, like the agent: you can vendor it into your own tooling.

## Signing in

1. Run `kubemg login --server https://kubemg.example.com`. It prints a code like `BCDF-GHJK` and opens your browser at **/cli/activate** with the code filled in. On a machine with no browser, open the printed address yourself, on any device. If your system does not trust the server's certificate — a self-signed install — it first shows the certificate and asks you to confirm it; see [Trusting the server's certificate](#trusting-the-servers-certificate).
2. Sign in to the console if you are not already — with your second factor, if your account has one. The sign-in page says a command line is waiting, and brings you to the approval afterwards.
3. Check what the page shows: **the code, which must match the one in your terminal**, the machine name, the system, **the address the request came from**, how long the session will last, and the account it will sign in as. If the address is not where you are, the page says so. To approve as somebody else, **Use another account** signs you out and brings you back to the same code.
4. **Approve sign-in.** The terminal finishes on its own within a few seconds, and the tab can be closed.

!!! warning "Only approve a sign-in you started just now"
    Approving gives the command line that asked *your* access. Somebody who starts a sign-in and sends you the link gets your clusters if you approve it. When in doubt, **Deny** — the CLI is told no and nothing is signed in.

A browser session that did not pass a second factor cannot approve when your account holds one, or must hold one: sign out, sign in again with the factor, and approve from the new session.

A code waits ten minutes. One address can have at most ten sign-ins waiting at a time.

## Adding clusters to your kubeconfig

```bash
kubemg clusters                    # what you can reach (also: kubemg cluster list)
kubemg update-kubeconfig           # pick from that list
kubectl get pods
```

With no cluster named, `update-kubeconfig` lists the clusters you can reach and asks which to add:

```text
$ kubemg update-kubeconfig
Clusters dana can reach on https://kubemg.example.com:

  1)  edge-us   production  view  payments
  2)  edge-eu   staging     edit  all
      legacy    production        not through the CLI — registered for direct API access

Add which? Numbers like 1,3 — or all. Enter adds none: 1,2
Added 2 contexts to /home/dana/.kube/config:
  kubemg.example.com/edge-us
  kubemg.example.com/edge-eu
Switch with: kubectl config use-context kubemg.example.com/edge-us
```

To skip the question, name the clusters — `kubemg update-kubeconfig edge-us` (or `--cluster edge-us`, by name or numeric id) — or add every one you can reach with `--all`. A single cluster becomes the current context; adding several leaves the current context alone. A script never gets the question: with no terminal and no cluster named, the command stops and says to name one or pass `--all`.

`kubemg clusters -o json` prints the same list as JSON — `id`, `name`, `environment`, `connection_mode`, `role`, `namespaces` (empty means every namespace) and `cli` (whether the CLI can reach it) — for scripts.

`update-kubeconfig` writes three entries — the cluster (the console's proxy address and, for a server whose certificate is not publicly trusted, the CA to verify it with), a user that runs `kubemg credential`, and a context — into the first file in `$KUBECONFIG`, or `~/.kube/config`. Everything else in the file, comments included, is left as it was, and the previous version is kept beside it as `config.bak`. Running it again replaces kubemg's entries rather than adding more.

Every entry is named under the server's host as you typed it at login — `kubemg.example.com/edge-us` for the cluster and context, `kubemg.example.com/edge-us/dana` for the user — and carries a `kubemg` extension naming its server. kubemg only ever replaces an entry carrying its own marker for the same server: if a name it would write is already taken by an entry it did not write, it stops, changes nothing, and names the entry. A server therefore cannot point one of your existing contexts, users or clusters somewhere else.

| Flag | |
|---|---|
| `NAME\|ID …` or `--cluster NAME\|ID` | Which clusters. Without one, a terminal is asked; a script is refused |
| `--all` | Every cluster the CLI can reach. Direct-mode clusters are skipped and named |
| `--namespace NS` | The context's namespace. Defaults to the first one your grant covers; one outside it is refused |
| `--alias NAME` | Name the context something else. One cluster only |
| `--kubeconfig FILE` | Write to this file instead |
| `--no-set-context` | Add the context without making it current |
| `--static --ttl 8h` | Write a kubeconfig that carries its own token instead of running kubemg — the console download, for a tool that cannot run a credential plugin. It is registered like any download and refused under `cli_only`. Only the token is taken from what the server sends; the entries are written by kubemg itself, so a server can never put a command into your kubeconfig |
| `--server URL` | Needed only when you are signed in to more than one server (or set `KUBEMG_SERVER`) |

The entry kubectl runs looks like this, and needs no editing:

```yaml
users:
- name: kubemg.example.com/edge-us/dana
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

## Trusting the server's certificate

kubemg talks to its server over **https only** — the session's refresh token is a credential, and kubectl would not use a plaintext server anyway. It writes only https cluster addresses into a kubeconfig, and opens the approval page in your browser only when it is an https address (anything else is printed for you to follow). **There is no flag that turns certificate verification off.** What `kubemg login` trusts is decided once, at sign-in, in one of three ways:

1. **A publicly trusted certificate** — or one your system already trusts, such as a company CA installed on the laptop. Nothing to do.
2. **Confirming it at the first sign-in**, the way `ssh` asks about a new host. When your system does not trust the certificate, `kubemg login` shows it before sending anything:

    ```text
    The certificate https://kubemg.internal presents is not trusted by this system.

      Subject:     CN=kubemg
      Issued by:   CN=kubemg (self-signed)
      Valid until: 16 Oct 2027
      SHA-256:     3F:A2:…:9C

    Compare the fingerprint with the one the console shows under My credentials → Use the command line,
    or the one your administrator gave you.

    Trust this certificate for https://kubemg.internal? [y/N]
    ```

    Answer **y** only if the fingerprint matches. The console shows it under **My credentials → Use the command line** and in the kubeconfig sheet's command-line section whenever kubemg serves a certificate of its own (a self-signed one, or one it pins into agent installs); an administrator can also read it off the certificate file with `openssl x509 -in tls.crt -noout -fingerprint -sha256`. The certificate is then kept with the session and is **the only one trusted** for that server from then on — by kubemg and by every kubeconfig entry it writes. Later sign-ins ask nothing while the server presents the same certificate.

3. **`--ca-file`**: `kubemg login --server https://kubemg.internal --ca-file kubemg-ca.pem` trusts that CA without asking — the way to do it from a script, and for a company CA presented with intermediates. It is kept with the session the same way.

The question is never asked without a terminal: a script that meets an untrusted certificate stops, shows the fingerprint and says to pass `--ca-file`. A certificate for a different name than the one you typed is refused outright rather than offered, as is one that is expired.

!!! warning "When the certificate changes"
    If the server presents a different certificate from the one you trusted at your last sign-in, `kubemg login` says so in a **WARNING** before asking again. A planned renewal causes this; if nobody renewed the certificate, somebody may be intercepting the connection — answer no and ask your administrator.

When the server names a CA for its clusters (a self-signed install does), that CA goes into the kubeconfig. When it names none, the CA the session was signed in with — the one you confirmed or passed with `--ca-file` — goes in instead, so kubectl verifies the server exactly as kubemg does.

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
