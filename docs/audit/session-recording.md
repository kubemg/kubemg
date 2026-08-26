# Session recording

The audit trail says a shell was opened in a production pod. A recording is
what was *done* in it. Every `exec` and `attach` proxied through the bastion
is teed into a replayable cast, on by default, and — because it holds
production output and, unless you turn it off, every keystroke — treated
throughout as the most sensitive artefact kubemg writes.

## What is captured

`pkg/terminal` writes recordings as **asciinema v2**: a JSON header line
followed by one JSON array per event (`[offset, code, data]`), gzip
compressed, stored as `.cast.gz`. That format is deliberate — a recording
plays in the `asciinema` player itself, not only in kubemg's own, and being
a text stream it compresses to a fraction of its size and needs no index to
start playing from the beginning.

A recording is a **tee**, not a second session: `pkg/bastion/exec.go`
already carries the Kubernetes exec/attach channel protocol
(stdin/stdout/stderr/error/resize, channel-prefixed) verbatim between the
client and the cluster, and `recordFromCluster`/`recordFromClient` read the
channel prefix off the same bytes without disturbing them:

- Channel `1`/`2` (stdout/stderr) become `o` (output) frames — the two are
  one stream in a replay, exactly as they are on a real terminal.
- Channel `0` (stdin) becomes an `i` (input) frame, unless keystroke
  capture is off (see below).
- Channel `4` (resize) becomes an `r` frame, so a replay reflows the way the
  operator's window did.
- Channel `3` — the API server talking *about* the session, not the session
  itself — is excluded entirely.

**`port-forward` is never recorded.** It carries arbitrary TCP, not a
terminal, and there is nothing meaningful to replay — recording it would
mean storing an opaque byte stream nobody can read back as anything.

Nothing here can refuse or slow a session. `Begin` returning `nil` just
means "this session is not being recorded"; a disk that stops accepting
writes ends the recording in place and leaves the shell running — a gateway
that killed a production shell because a volume filled up would be a worse
product than one with a gap in its recordings and a line in the log saying
so.

## Where files go

`KUBEMG_SESSION_RECORDING_DIR` (default `/var/lib/kubemg/recordings`) is
where `.cast.gz` files land, laid out as
`{dir}/cluster-{id}/{YYYY-MM-DD}/{session-id}.cast.gz`. **Mount it.**
Recordings have to outlive the container; an unmounted directory means every
recording vanishes on the next rollout. The directory is created `0700` and
every file `0600` — that protects the files from other processes on the
host, and from nothing else: a volume snapshot, a misconfigured backup, a
debug container mounting the same PVC, or root on the node all read an
unencrypted recording in the clear. See [Encryption at rest](#encryption-at-rest).

## Sizing and truncation

`KUBEMG_SESSION_RECORDING_MAX_BYTES` caps one recording (default 32 MiB,
`terminal.DefaultMaxBytes`). Past the cap, the recorder writes one visible
truncation frame — `[kubemg] recording truncated: this session exceeded the
per-recording limit` — and drops the rest silently rather than growing an
unbounded file. A shell that printed a gigabyte is almost always a `cat` of
something large; holding the first 32 MiB answers "what did they do" and the
truncation flag says the rest was dropped. `terminal_sessions.truncated`
records it per row, and `TerminalSessionPlayer` surfaces it in the replay
UI.

## Keystroke capture

`KUBEMG_SESSION_RECORDING_INPUT` (default `true`) controls whether keystrokes
are recorded at all. The field that drives it is named the negative way on
purpose (`Options.OmitInput`) — the zero value has to keep recording input,
because that is what every existing deployment already does, and a silent
switch to less evidence than an auditor expects would be worse than an
awkwardly-named field.

Turn it **off** (`KUBEMG_SESSION_RECORDING_INPUT=false`) where operators
routinely type credentials into interactive tools. A pty echoes what was
typed, so most of a session's input is already visible in its output
stream — what dropping input actually loses is precisely the part a prompt
deliberately does *not* echo: `mysql -p`, `vault login`, a token pasted into
an interactive tool. That is exactly the part worth not storing if you'd
rather not hold it. `terminal_sessions.input_recorded` is stamped per row,
so an empty keystroke view in the replay UI is distinguishable from "nothing
was typed into this particular session."

## Encryption at rest

A recording is the most sensitive artefact this product writes, and
encryption is not optional plumbing — it is the difference between a stolen
volume snapshot being an inconvenience and it being every password anyone
typed into a production shell for the life of the recording window.

### Generating a key

```bash
openssl rand -base64 32
```

Set the result as `KUBEMG_SESSION_RECORDING_KEY`. `terminal.ParseKey`
accepts exactly 32 raw bytes as hex or base64 (any of standard, raw
standard, URL, or raw URL base64) and refuses anything else — including a
passphrase, which would need a KDF and a stored salt, and would invite a key
with only a few bits of real entropy protecting the most sensitive file on
the volume.

The key lives in the **environment**, never in the database. The database
is the one thing that is always backed up alongside the recordings volume;
a key kept next to the ciphertext protects against nothing. Losing the key
means losing the recordings — that is the trade every at-rest encryption
scheme like this one makes, and it is deliberate here too.

### The construction

Recordings are encrypted as a **chunked AES-256-GCM stream** — the shape
`age` and TLS use — rather than one AES-GCM seal over the whole file:

- A recording is written incrementally over what may be hours; sealing one
  blob would mean holding the whole session in memory and losing everything
  written so far if the process died mid-session.
- Playback streams — a reader has to authenticate and hand over the first
  chunk without having read the last one yet.

Each chunk (64 KiB of plaintext) carries its own nonce, derived from an
8-byte random prefix generated once per file plus a 4-byte counter. The
chunk's sequence number and its end-of-stream flag are both authenticated
as additional data, which is what turns **reordering, dropping, duplicating,
or truncating chunks into a decryption failure** rather than a shorter but
still-readable recording — an audit artefact that can be silently trimmed
is not one.

Compress-then-encrypt is deliberate, not an oversight: the compression-
oracle family of attacks (CRIME and relatives) that makes that ordering
dangerous needs an attacker who can inject chosen plaintext into the stream
*and* repeatedly observe its compressed size — a file written once to disk
offers neither. Encrypting first would mean storing incompressible data,
and an encrypted recording that no longer compresses runs at roughly ten
times the size an operator has to provision storage for.

### Failure modes, and what each one means

A key of the **wrong length** stops recording rather than silently writing
plaintext (`NewRecorder` refuses to start if a configured key is not 32
bytes). An operator who set a key believes the files are encrypted; writing
them in the clear because the key parsed wrong would betray that belief
without saying so.

A **missing** key — no `KUBEMG_SESSION_RECORDING_KEY` at all — is different:
it is the documented default and the state every install starts in before
somebody sets one, so it warns loudly at boot (`session recordings are
being written unencrypted; they hold production shell output and
keystrokes`, naming the fix) and carries on recording anyway.

Reading a recording back distinguishes three failure answers, and they call
for three different operator actions:

| Error | What it means | What to do |
|---|---|---|
| `ErrKeyRequired` | The file is encrypted (its magic says so) and this server has no key configured | The evidence still exists. Restore `KUBEMG_SESSION_RECORDING_KEY` |
| `ErrKeyMismatch` | The file will not authenticate with the configured key — the wrong key, or the file has been altered | These two causes are deliberately not distinguished: AEAD cannot tell them apart, and neither answer changes what you do next. Check you have the right key; treat an altered file as a security incident |
| `ErrTruncated` | The stream ends without its final authenticated chunk | The recording was cut off — a crash, a killed process, a copy that did not finish. What is missing cannot be recovered; the bytes that are present are still authentic |

### Key rotation and backup

There is no built-in rotation mechanism because there is nothing to
rotate against — each file is sealed once, at write time, with whatever key
was configured then. Rotating in practice means: configure the new key,
restart, and new recordings use it. **Old recordings still need the old
key to read back** — whether a given file is encrypted, and with what
generation of key, is read from the file's own magic bytes, never from
current configuration (see [Detecting encryption from the file](#detecting-encryption-from-the-file)
below), so nothing about switching keys retroactively touches what is
already on disk.

Because losing the key loses the recordings permanently, back it up
somewhere that is **not** alongside the recordings volume or the database —
if a single backup captures both, that backup is exactly as exposed as an
unencrypted volume would have been. Treat it like any other root secret:
a secrets manager, not a file next to the PVC.

### Detecting encryption from the file

Whether a given `.cast.gz` file is encrypted is decided by reading its
first 8 bytes against a fixed magic string (`KMGCAST1`), never by asking the
current configuration. That is what lets a key be turned on mid-deployment
without orphaning every recording written before it existed — an old,
unencrypted file is still read correctly by a server that now has a key
configured, and a newly-encrypted one is read correctly even if the key
changes later, as long as the right key for that file is available.
`terminal_sessions.encrypted` records which one each row actually is, so the
recordings index can say so per session rather than assuming.

## Who may watch a recording

Everyone may always replay their **own** sessions. Watching **someone
else's** needs the recording-viewer capability
(`db.User.CanViewRecordings`, surfaced as `MayViewAllRecordings()`) on top
of the admin role — being able to administer kubemg is not the same claim
as being able to watch what a colleague typed into production. The
capability is:

- Held implicitly by a super admin.
- Grantable to anyone else **only by a super admin** — otherwise an admin
  could grant it to themselves and the control would be theatre.
- Never granted by giving it to a non-admin account; it governs nothing
  there.
- Never needed for, and never governs, a caller's own sessions.

Existing admins are grandfathered into the capability **exactly once**, on
the upgrade that introduces it, guarded by a `recording_access_backfilled`
setting row — without that marker the backfill would silently re-grant on
every boot a capability an administrator had deliberately revoked from
someone.

Reaching someone else's recording answers **404, not 403**. Whether a
recording of a given colleague's shell exists is not something the caller
has any business learning, so a refused lookup looks identical to a
recording that never existed.

## Watching and deleting are themselves audited

Every read of the *content* of a recording — a replay, a metadata fetch of
someone else's session, a deletion — writes its own audit record under the
verbs `replay`, `recording-get`, `recording-delete`, **before the bytes or
the file move**, refusals included. This is not incidental; it is the
whole point of the record: a surveillance capability with no trail of its
own would be the first thing an auditor asks about. The identities on that
record are deliberately crossed — the record's `user_id` is the *viewer*,
and its `session_id` is the *subject's* session — because neither half
answers "who watched whose shell" on its own.

Listing the recordings **index** is deliberately not audited. An index is
metadata an administrator needs to do the job at all, and recording that
routine act too would bury the genuinely invasive one — opening a specific
replay — among a hundred ordinary list requests.

## The runtime switch

`record_exec_sessions` (surfaced from `pkg/auditpolicy`) can turn session
recording **off** at runtime, from Settings. It can only ever turn it off:
a process started with no recording directory or no working recorder
configured has nowhere to write, and no database row changes that. Flipping
it stops the **next** shell that opens from being recorded and leaves any
session already running alone — the only behaviour that never produces a
recording with a silent gap in the middle of it.

## Retention

`session_recording_retention_days` defaults to the audit window
(`audit_retention_days`) and is **capped by it**
(`clampRecordingRetention`): a stored value of zero (or missing) takes the
audit window, and a stored value *longer* than the audit window is clamped
down to it rather than refused. The direction matters — a recording is
audit evidence about a line in the trail, so a replay outliving the record
that says the shell was ever opened would be evidence with nothing to
correlate it to. Clamping rather than refusing on write matters too: the
audit window itself is editable, so shortening it has to pull the
recording window in with it, and a value that was legal when it was saved
must not turn into a validation error nobody can see later.

The same background pass that prunes the audit trail
(`pkg/api/audit_prune.go`) removes recordings past their window, **rows and
files together, in one pass** — a row deleted with its file left behind, or
a file deleted with its row left behind, is exactly the orphaned state
nothing will ever clean up on a later pass.

## Disclosure

`GET /api/v1/audit/recording-policy` reports `enabled`, `input_recorded`,
`encrypted`, and `retention_days`, and is readable by **anyone** — not just
admins. Anyone might be recorded, and a console that is about to open a
shell has to be able to say what it keeps before the first keystroke goes
in; "go ask an administrator" is not an answer that arrives in time.
`PodTerminal` draws this as a persistent line in the terminal UI rather
than a dismissible dialog, because a dialog gets dismissed by reflex within
a week and the point is that the fact stays visible for as long as the
shell is open.

## Playing a cast outside kubemg

An **unencrypted** recording is plain gzip holding a standard asciinema v2
stream — `gunzip` it and it plays in the `asciinema` CLI or any player that
understands the format, without ever going through the console.

An **encrypted** recording cannot be decrypted outside kubemg — there is no
standalone decryption tool shipped, by design: the key lives only in the
server's environment, and a detachable decryption path would be a second
place the most sensitive artefact in the product could leak from. What the
console offers instead is the one supported path: `GET
/api/v1/audit/terminal-sessions/:id/stream`, which decrypts, decompresses,
and streams the plain asciinema bytes to an authorized viewer — the same
authorization, 404-not-403, and audit-before-bytes rules described above
apply to that stream exactly as they do to a replay in the UI. If you need
an exported, portable cast file, capture it from that authorized stream
rather than from the file on disk.
