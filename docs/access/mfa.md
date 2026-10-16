# Two-factor authentication

A second factor makes a password alone not enough to open a kubemg account. After the password, sign-in asks for one of two things:

- a **security key or passkey** — a YubiKey or other FIDO2 key, Touch ID, Windows Hello, or a phone's passkey (WebAuthn). One touch, and it cannot be phished: the key is bound to kubemg's address and does not answer a look-alike page;
- a six-digit code from an **authenticator app** (1Password, Google Authenticator, Microsoft Authenticator — anything that speaks TOTP).

An account can hold several of either. Administrators decide who must have one under **Admin → Settings → General → Second factor**.

## Which accounts it covers

| Account | kubemg's second factor |
|---|---|
| Local (a password held by kubemg) | Yes |
| LDAP | **Yes.** A directory bind checks a password and nothing else, so an LDAP account is exactly as exposed as a local one. |
| OIDC or SAML | No. The identity provider owns the second factor; ask for it there. kubemg never sees it and does not ask again. |
| Machine account | No. It has no password — it signs in with a stored, revocable token. |

## Setting one up

From **My profile → Two-factor authentication → Set up**, choose **Security key or passkey** or **Authenticator app**, and name the factor (for example "YubiKey" or "Work phone") so two of them can be told apart later.

For a **security key or passkey**: select **Register key**, and answer the browser's prompt — touch the key, or confirm with this device's fingerprint, face or PIN. The factor counts once the browser hands kubemg what the key made.

For an **authenticator app**: scan the QR code with the app, or type the setup key under it, then type the six digits the app shows. The factor counts only once this code is accepted.

The **first** factor comes with ten recovery codes. They are shown once. Copy or download them, tick **I have saved these codes**, and keep them somewhere other than the phone or key.

Adding a second factor (a backup key, a second phone, a password manager) needs a proof of the first — a current code, or a touch of a key you already hold — so somebody holding a stolen session cannot add their own.

### Where a security key works

A key is bound to the **host of kubemg's public URL** (**Admin → Settings → General → Server URL**) when it is registered, and the browser will use it there and nowhere else. That is what makes it phishing-resistant, and it has three consequences:

- **Open the console at its public URL** to register or use a key. A console reached at another address — an internal hostname, a port-forward — is told where to go instead.
- The public URL must be **https** and a **domain name**. Browsers refuse security keys on a plain-http address (except `localhost`) and on an IP address; the profile says which, and offers the app instead.
- **Changing the public URL's host retires every key registered under the old one.** They stay listed on their owner's profile, marked with the host they belong to, but sign-in no longer offers them. Their owners sign in with another factor or a recovery code, register the key again and remove the old entry. An account whose only factor was a key and who has no recovery codes left needs an administrator's reset.

kubemg does not ask a key for its make and model (no attestation), so any FIDO2/WebAuthn authenticator is accepted.

An enrolment that is started and not confirmed within fifteen minutes is discarded.

## Signing in

After a right password, the sign-in card asks for the factor. When the account holds a key that works here, **Use security key** comes first; **Use the authenticator app** and **Use a recovery code** are under it, when the account has them.

A key's answer is good for that one sign-in only. A key also counts its signatures, and kubemg refuses a signature whose count went backwards — the sign of a copied key. (Synced passkeys keep no count; that is accepted.)

An app's code is accepted once: the same six digits cannot sign in twice, even within their 30 seconds. Codes from one step either side of now are accepted, so a phone whose clock is a few seconds off still works.

**Use a recovery code** appears when the account has some left. Each code works once. A sign-in finished this way says how many remain; make new ones from the profile before they run out.

Wrong codes are slowed down exactly like wrong passwords: a few are free, then each further attempt has to wait, and the console says how long.

## Managing your factors

On **My profile**:

- **Remove** a factor — needs a current code (from any app, or a recovery code) or a touch of a key you hold. Removing the last one takes the recovery codes with it, and sign-in goes back to a password alone. If your organisation requires a factor, the last one cannot be removed; add another first.
- **New recovery codes** — needs a current code or a key. Ten new codes replace the old set, used or not.

## The command line

`kubemg login` never asks for a code itself: it is approved in the browser, from a session that already passed the factor. A session that did not cannot approve when the account holds a factor or must hold one. See [The kubemg command line](cli.md).

## Requiring a second factor

**Admin → Settings → General → Second factor**, stored as the `mfa_enforcement` setting:

| Value | Who must have a factor |
|---|---|
| Optional (`off`, the default) | Nobody. Anyone covered may set one up. |
| Administrators (`admins`) | Administrators and super admins. |
| Everyone (`all`) | Every local and LDAP account. |

An account that must have a factor and has none is not let in with its password: the sign-in card walks it through setting one up — a key or an app, where keys work — and the session opens only after the factor is confirmed and the recovery codes are acknowledged.

Requiring a factor does **not** end sessions already open — they run to their own expiry (`JWT_TTL`, 12 hours by default). The next sign-in is the one that asks.

The **Deployment** posture list warns while the setting is Optional, and names how many super admins still sign in with a password alone.

## Someone lost their phone or key

An administrator opens the person's page (**Admin → Users →** their name) and uses **Reset second factor** in the Identity panel. Every factor and recovery code the account holds is removed; if a factor is required, the next sign-in goes straight to setting up a new one. Confirm it is really them first: this is the step an attacker asks a helpdesk for.

- Nobody can reset their own second factor — remove a factor from your profile instead.
- Only a super admin can reset a super admin.
- The page shows each account's second factor in its access review: how many authenticator apps and security keys, how many recovery codes left, or that an OIDC/SAML account's factor is its provider's.

## Break-glass: the only super admin lost both phone and codes

Set `KUBEMG_MFA_RESET_USERNAME` to that account's username and restart kubemg. At boot the account's second factor is removed, an `ERROR` line is logged and a `mfa-reset` record is written to the audit trail under the user `kubemg:boot`. Sign in, set up a factor again, then **remove the variable and restart**: while it is set the reset repeats at every start, and the posture list warns about it.

## In the audit trail

| Verb | When |
|---|---|
| `mfa-enroll` | A factor was confirmed. The path ends `?kind=totp` or `?kind=webauthn` |
| `mfa-remove` | A factor was removed by its owner, with the same `?kind=` |
| `mfa-reset` | An administrator (or the boot break-glass) removed somebody's factors. The administrator is the user; the account reset is in the impersonated-user column |
| `mfa-recovery-used` | A recovery code was spent |
| `mfa-recovery-regenerate` | A new set of recovery codes replaced the old one |

`mfa-reset`, `mfa-remove` and `mfa-recovery-used` are recorded whatever the trail's verb selection says, and can be used in [alarm rules](../audit/alarms.md). No secret, code or key credential is ever recorded.

## Routes

| Route | Auth | Purpose |
|---|---|---|
| `POST /api/v1/auth/login` | none | A right password answers `{mfa_required, challenge}` or `{mfa_enrollment_required, challenge}` instead of a token when a factor is owed |
| `POST /api/v1/auth/mfa/verify` | challenge | `{challenge, code}` or `{challenge, webauthn}` → the session |
| `POST /api/v1/auth/mfa/challenge/webauthn` | challenge | Open a security-key check for a sign-in |
| `POST /api/v1/auth/mfa/webauthn` | session | Open a security-key check to prove a change to your factors |
| `GET /api/v1/auth/mfa` | session | Your own factors and recovery codes left |
| `POST /api/v1/auth/mfa/enroll`, `.../enroll/confirm` | session | Add a factor |
| `POST /api/v1/auth/mfa/challenge/enroll`, `.../challenge/enroll/confirm` | challenge | Set up the factor a sign-in requires |
| `DELETE /api/v1/auth/mfa/factors/:id` | session + code or key | Remove a factor |
| `POST /api/v1/auth/mfa/recovery-codes` | session + code or key | New recovery codes |
| `GET /api/v1/users/:id/mfa`, `POST /api/v1/users/:id/mfa/reset` | admin | Read or reset somebody else's |
