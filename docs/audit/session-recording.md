# Session recording

Every `exec` and `attach` that goes through kubemg is recorded as a replayable cast, on by default. The audit trail says a shell was opened in a production pod. The recording shows what was done in it. This page covers where recordings go, how they are protected and who may watch them.

Recordings hold production output and, unless you turn it off, every keystroke, so treat them as the most sensitive thing kubemg writes.

## What is captured

Recordings are **asciinema v2** casts, gzip-compressed as `.cast.gz`. They play in the `asciinema` player as well as in kubemg.

- Output (stdout and stderr) is one stream, as on a real terminal.
- Keystrokes are recorded unless capture is off (below).
- Window resizes are recorded, so replay reflows like the operator's window.
- **`port-forward` is never recorded.** It carries arbitrary TCP, not a terminal.

Recording is a copy of the session, not a second session. It cannot refuse or slow a shell. If the disk stops accepting writes, the recording ends in place and the shell keeps running.

<figure markdown>
  ![A session recording replaying](../assets/screenshots/recording-replay.png)
  <figcaption>A replay. Output is drawn here; keystrokes, when they are captured, are a tab of their own.</figcaption>
</figure>

## Where files go

| Setting | Default | Meaning |
|---|---|---|
| `KUBEMG_SESSION_RECORDING_DIR` | `/var/lib/kubemg/recordings` | Where `.cast.gz` files land, as `{dir}/cluster-{id}/{YYYY-MM-DD}/{session-id}.cast.gz` |
| `KUBEMG_SESSION_RECORDING_MAX_BYTES` | 32 MiB | Cap on one recording |
| `KUBEMG_SESSION_RECORDING_INPUT` | `true` | Whether keystrokes are recorded |
| `KUBEMG_SESSION_RECORDING_KEY` | unset | 32-byte key that encrypts recordings at rest |

**Mount the directory.** Without a volume, every recording vanishes on the next rollout. The directory is created `0700` and files `0600`. That only protects against other processes on the host. A volume snapshot, a backup or root on the node can still read an unencrypted recording, so set a key.

Past the size cap, a visible `[kubemg] recording truncated` frame is written and the rest is dropped. The replay shows that it was truncated.

## Keystroke capture

Turn input **off** (`KUBEMG_SESSION_RECORDING_INPUT=false`) where operators type credentials into interactive tools. Most typed input is echoed in the output anyway. What you lose is exactly what a prompt does not echo, such as `mysql -p`, `vault login` or a pasted token. Each recording is stamped with whether input was captured, so an empty keystroke view is distinguishable from "nothing was typed".

## Encryption at rest

Generate a key and set it as `KUBEMG_SESSION_RECORDING_KEY`:

```bash
openssl rand -base64 32
```

It must be exactly 32 bytes, as hex or base64. A passphrase is refused.

- Keep the key in the environment or a secrets manager, never next to the recordings volume or the database. **Losing the key loses the recordings**, so back it up separately.
- **A key of the wrong length stops recording** rather than writing plaintext.
- **No key at all** is the default. kubemg logs a loud warning at boot and records unencrypted.
- Whether a file is encrypted is read from the file itself, not from current configuration. Turning a key on later does not orphan older recordings.
- There is no rotation. After you configure a new key and restart, new recordings use it, and **old recordings still need the old key**.

Reading back distinguishes three failures:

| Error | Meaning | What to do |
|---|---|---|
| Key required | The file is encrypted and this server has no key | Restore `KUBEMG_SESSION_RECORDING_KEY` |
| Key mismatch | The file will not authenticate with the configured key: wrong key, or the file was altered | Check the key. Treat an altered file as a security incident |
| Truncated | The stream ends early (a crash, a killed process, an unfinished copy) | The missing part cannot be recovered. What is present is still authentic |

??? info "Why it works this way"
    Recordings are a chunked AES-256-GCM stream (64 KiB chunks), because a session is written over hours and played back as it is read. Each chunk's position and an end-of-stream flag are authenticated, so reordering, dropping or truncating chunks fails to decrypt instead of replaying a shorter recording. Data is compressed before encryption. The attacks that make that order risky need an attacker who can inject text into the stream and observe its size, which a file written once does not allow. Encrypting first would store incompressible data at roughly ten times the size. The key is kept out of the database because the database is always backed up alongside the recordings.

## Who may watch a recording

Everyone may replay their **own** sessions. Watching someone else's needs the **recording-viewer** capability on top of the admin role.

- A super admin holds it implicitly.
- Only a **super admin** can grant it to others, so an admin cannot grant it to themselves.
- Granting it to a non-admin account does nothing.
- Admins existing at the upgrade that introduced it were granted it once. A later revoke is not undone by a restart.

Someone else's recording answers **404, not 403**, so the caller cannot learn whether it exists. Deleting a recording is admin-only, and never while it is running.

## Watching and deleting are audited

Every replay, metadata fetch of someone else's session, and deletion writes its own audit record (`replay`, `recording-get`, `recording-delete`) before anything is read or moved, refusals included. On that record `user_id` is the **viewer** and `session_id` is the **subject's** session, so it answers who watched whose shell. Listing the recordings index is deliberately not audited.

## The runtime switch

`record_exec_sessions` in Settings can turn recording **off**, never on. If the server started with no recording directory, nothing in the database can enable it. Turning it off affects the next shell only, and leaves running sessions alone.

## Retention

`session_recording_retention_days` defaults to the audit window (`audit_retention_days`) and is **capped by it**. Zero or missing takes the audit window, and a longer value is clamped down, not refused. A recording must not outlive the audit record that says the shell was opened. The audit pruning pass removes the row and the file together.

## Disclosure

Anyone, not just admins, can read the recording policy: whether recording is on, whether input is captured, whether it is encrypted, and the retention. The terminal shows it as a persistent line for as long as the shell is open, so people know before the first keystroke.

## Playing a cast outside kubemg

An **unencrypted** recording is plain gzip holding an asciinema v2 stream. `gunzip` it and play it in the `asciinema` CLI.

An **encrypted** recording cannot be decrypted outside kubemg, by design, since a detachable decryption tool would be a second place to leak from. Use the console's replay, which decrypts for an authorised viewer under the same 404 and audit rules. To get a portable cast, capture it from that stream.
