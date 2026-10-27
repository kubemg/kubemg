# Password expiry

kubemg can make passwords expire: after a number of days you choose, a person
has to pick a new one before they can do anything else in the console. People
are emailed before theirs runs out, and an administrator can expire one
account's password by hand at any time.

## Which accounts it covers

| Account | Governed by password expiry |
|---|---|
| Local (a password held by kubemg) | Yes |
| LDAP, OIDC or SAML | No. The password belongs to the directory or identity provider; set its policy there. |
| Machine account | No. It has no password — it signs in with a stored, revocable token. |

## Turning it on

Under **Admin → Settings → General → Password expiry**, enter how many days a
password lasts (1 to 3650). Leave it empty for no expiry, which is the default.

The count starts from each password's **last change** — by its owner, or by an
administrator setting it on the account. Accounts whose password was set before
your kubemg version had this feature count from the upgrade, so turning the
policy on never finds every old account already expired.

The setting takes effect at once. Shortening it can put some passwords past due
immediately; their owners are held at the change screen at their next click.

## What happens when a password expires

The expired password **still signs in** — it was the right password — and the
console then shows only one screen: **Choose a new password**, asking for the
current password, the new one, and the new one again. Nothing else opens until
the password is changed. Somebody who is already signed in when their password
expires (or is expired by an administrator) is taken to the same screen at their
next click.

Two things are deliberately not affected:

- **Kubeconfigs and `kubemg` command-line sessions** the person already holds
  keep working until they expire or are revoked. If you are expiring a password
  because it may have leaked, also revoke those from **Admin → Issued
  credentials**, or ask the person to tick *Revoke my issued kubeconfigs* when
  they change it from their profile.
- **A second factor** is still asked for at sign-in, before the change screen.

The new password follows the same rule as any other: at least 8 characters, and
not the same as the current one.

## Expiring one account's password by hand

Open the account under **Admin → Users**. The *Identity* panel shows when its
password was last changed and when it expires; **Expire password** makes the
person choose a new one before anything else — at their next sign-in, or at
their next click if they are signed in now.

- You cannot expire your own password — change it from **My profile** instead.
- Only a super admin can expire a super admin's password.
- The button is absent for an LDAP, OIDC, SAML or machine account.

Setting a new password for somebody on their account does **not** lift an
expiry: that password is one you know, so they are still asked to replace it.
Only their own change does. A typical reset for a person who forgot their
password is therefore: set a temporary password, then **Expire password**, then
give them the temporary one.

The user list marks every account whose password has expired.

## Email reminders

People are mailed **14, 7, 3 and 1 days** before their password expires, and
once when it has. An account expired by an administrator gets one message saying
so straight away. Each reminder is sent once; a new password starts the
reminders again for its own expiry date.

Reminders need two things:

- a **mail relay**, configured under **Admin → Settings → Email** (below). With
  none, passwords still expire on schedule — nobody is warned. The General
  settings page says so while the policy is on and no relay is set.
- an **email address on the account**. An account without one gets no
  reminders; people can set their own under **My profile**.

The message names the account and the date and points to **My profile**, where
the password is changed. It never contains a sign-in link.

### The mail relay

Under **Admin → Settings → Email**:

| Field | |
|---|---|
| Host | The SMTP relay's host name or address, without a scheme or port. |
| Port | Usually 587 for STARTTLS, 465 for TLS, 25 for none. |
| Security | **STARTTLS** (default) upgrades the connection before anything is sent; a relay that does not offer it is refused, never used in plain text. **TLS** is encrypted from the first byte. **None** is plain text, and only for a relay that needs no login — a password is never sent unencrypted except to a relay on the same host. |
| Sender address | The *From* of every message, for example `KubeMG <kubemg@example.com>`. |
| Username / Password | For a relay that needs a login. The password is stored encrypted (under `KUBEMG_SECRET_KEY` when set) and never shown again; leave the field empty to keep it. A stored password is kept only while the host and username stay the same — change either and it has to be entered again, so the relay cannot be pointed somewhere else with the stored password still attached. |

Saving checks the values but does not contact the relay. **Send test message**
sends one message through the saved relay — to your own address unless you name
another — and shows the relay's own words if it refuses.

## In the audit trail

| Verb | Recorded when |
|---|---|
| `password-change` | Somebody changed their own password. |
| `password-expire` | An administrator expired somebody's password. The administrator is the user; the account is in the impersonated-user column. |

`password-expire` can be used in [alarm rules](../audit/alarms.md). No password
is ever recorded. Reminder emails are not audited; a relay that refuses one is
logged as a warning and tried again an hour later.
