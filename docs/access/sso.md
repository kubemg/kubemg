# Single sign-on

kubemg speaks three federation protocols — OIDC, SAML 2.0, and LDAP — configured at **Admin → Settings → SSO**. Local accounts keep working exactly as before; federation is additive. Everything a provider is worth in terms of kubemg access is decided by [group mappings](#group-mappings), not by the protocol engine itself — that split is what lets the console and a chat callback agree about authorization without either one re-implementing it.

## Setting one up

1. Sign in as an administrator and open **Admin → Settings → SSO**.
2. Add a provider and pick its protocol — [OIDC](#oidc), [SAML 2.0](#saml-20)
   or [LDAP](#ldap). The fields differ per protocol and are listed below.
3. Register kubemg's **redirect URI** (OIDC) or **SP metadata** (SAML) with the
   identity provider. Both are shown on the provider's own form.
4. Run **Check** on the saved provider. It performs a real read against the
   directory rather than a port check, so a green result means the credentials
   and the network path both work.
5. Add at least one [group mapping](#group-mappings). Until one matches,
   a directory can authenticate somebody perfectly and they will still land
   with no cluster access at all.
6. Sign out and confirm the provider's button appears on the login page.

## Provider kinds

All three protocols share one stored configuration shape rather than one per protocol, because they answer the same question — who is this, and what groups are they in — and differ only in how they ask it. Fields irrelevant to a given protocol stay empty.

### OIDC

The issuer is *discovered*, not configured field by field: paste the issuer URL and kubemg reads its `.well-known/openid-configuration` document (cached 15 minutes).

| Field | Notes |
| --- | --- |
| `issuer_url` | Required. Absolute `http(s)://` URL, trailing slash stripped. |
| `client_id` | Required. |
| `client_secret` | Write-only — omitted on an update keeps the stored one; sent empty clears it. |
| `scopes` | Space-separated, added to `openid`. Default: `profile email groups`. |
| `username_claim` / `email_claim` / `groups_claim` | Which claim carries each. Defaults: `preferred_username`, `email`, `groups`. If a claim is missing, kubemg falls back through `preferred_username → username → nickname → email → upn → sub` for the username and similar lists for email and groups — so a half-configured provider still works, but the fallback order is not a substitute for setting the right claim name. |
| `default_system_role` | `user` or `admin` for a newly provisioned account — never `superadmin`; that tier is what survives an IdP outage. |
| `allow_jit` | Provisions an account on first successful sign-in. Off means an unrecognised username is refused even with valid credentials. |

**Redirect URI** (register this with the IdP, byte for byte): `{public_url}/api/v1/auth/sso/providers/{id}/callback`.

Flow: authorization code with PKCE, always — even though kubemg holds a client secret. The code exchange happens server-to-server; the ID token is verified against the issuer's published keys; the nonce is checked against the one kubemg minted. If the ID token carries no groups (common — many IdPs keep groups out of the ID token to keep it small), kubemg calls the UserInfo endpoint as a fallback.

=== "Keycloak"

    Issuer URL is the realm's own base, e.g. `https://keycloak.example.com/realms/kubemg`. Create a confidential client with the redirect URI above, add a `groups` client scope (or a protocol mapper putting group paths into a `groups` claim), and put the client secret into kubemg's `client_secret`.

=== "Entra ID / Azure AD"

    Issuer URL is `https://login.microsoftonline.com/{tenant-id}/v2.0`. Register an app, add the redirect URI as a **Web** platform redirect, and add a client secret. Entra ID sends group *object IDs* by default unless "Emit groups as role claims" or a groups claim customization is configured — the mapping patterns you write have to match whatever form Entra actually sends (object IDs, or group display names if configured). Numeric subjects are handled natively (`toStrings` treats a `float64` claim as a stringified number).

=== "Okta"

    Issuer URL is the org's authorization server, e.g. `https://your-org.okta.com/oauth2/default`. Add an OIDC Web app with the redirect URI above; add the `groups` scope to the authorization server's scope list and a groups claim in its claims configuration if it is not already the default `groups` name.

=== "Google"

    Issuer URL is `https://accounts.google.com`. Google Workspace does not put group membership in the ID token or standard UserInfo response at all — `groups_claim` will not find anything from Google directly unless a directory sync product is fronting it. A mapping rule using pattern `*` (matches everyone the provider authenticates) is the practical way to grant a baseline role to any Google-authenticated sign-in when group-based rules cannot be used.

### SAML 2.0

kubemg is the service provider, SP-initiated, `HTTP-Redirect` out and `HTTP-POST` back. It sends an **unsigned** `AuthnRequest` (deliberately — the response is what carries the identity, and that is what is checked) and **requires the IdP's assertions be signed** (`WantAssertionsSigned: true` in kubemg's own SP metadata) — the assertion signature is verified against certificates published in the IdP's own metadata, and the audience must equal kubemg's entity ID. Neither check is configurable.

| Field | Notes |
| --- | --- |
| `saml_metadata_url` | Fetched and re-fetched (15-minute cache); most IdPs offer one. |
| `saml_metadata_xml` | A pasted document instead, for an IdP that hands out a file rather than a URL. One of the two is required. |
| `saml_entity_id` | What kubemg calls itself to this IdP. Defaults to the metadata URL below, which is unique and resolvable — override only if the IdP was registered years ago under a fixed name. |
| `username_claim` / `email_claim` / `groups_claim` | SAML has no universal attribute names, so kubemg tries the configured attribute name first, then both the "friendly name" vocabulary (`uid`, `mail`, `Group`) and the OASIS URN vocabulary AD FS and some Entra configurations send (`urn:oid:0.9.2342.19200300.100.1.1` for uid, `urn:oid:1.3.6.1.4.1.5923.1.5.1.1` for isMemberOf, and Microsoft's own claim URNs). |

**ACS (callback) URL**: `{public_url}/api/v1/auth/sso/providers/{id}/callback`
**Entity ID**: the configured `saml_entity_id`, or by default `{public_url}/api/v1/auth/sso/providers/{id}/metadata`
**SP metadata to upload into the IdP**: `GET {public_url}/api/v1/auth/sso/providers/{id}/metadata` (unauthenticated — an admin session viewing the provider list also sees these three URLs rendered, so nobody has to type them by hand)

=== "Keycloak"

    Keycloak can consume kubemg's SP metadata URL directly when adding a SAML client, or you can hand-enter the ACS URL and entity ID above. Configure a group membership mapper on the client to emit a `Group` (or `member-of`) attribute.

=== "AD FS / Entra ID (SAML)"

    Both send the long OASIS URNs by default (`http://schemas.xmlsoap.org/claims/Group`, `http://schemas.microsoft.com/ws/2008/06/identity/claims/role`) — kubemg already checks for these as SAML group candidates even with `groups_claim` left blank, but setting it explicitly to whatever claim rule was configured on the relying party trust avoids depending on the fallback order.

=== "Okta (SAML)"

    Okta's SAML apps typically send `Groups` (capitalized) when a Group Attribute Statement is configured on the app — matches kubemg's candidate list already. Upload kubemg's SP metadata into the app's SAML settings, or fill the ACS URL and entity ID manually.

### LDAP

The one non-interactive protocol: kubemg's own login form takes a username and password and checks them against the directory — there is no redirect and no assertion. `Interactive()` is `false` for LDAP for exactly this reason, and the console does not send it through `startSSOLogin`; it posts to `.../providers/:id/login` instead.

The bind order is fixed and matters: (1) bind as the service account (or anonymously), (2) find the user's entry and read groups, (3) bind **as the user with the supplied password, last**. Binding as the user first would leave the group search running as someone who may not be able to read their own memberships; binding in the middle would mean re-binding as the service account afterward, which is one more place to get credentials wrong. An **empty password is refused before it reaches the directory** — an LDAP bind with a DN and no password is an *unauthenticated bind*, which most directories answer with success, and a login form permitting it would accept any username with a blank password.

| Field | Notes |
| --- | --- |
| `ldap_host` / `ldap_port` | Port defaults to 636 (`ldap_use_tls`) or 389. |
| `ldap_use_tls` | Dials `ldaps://` — implicit TLS. Default `true`. |
| `ldap_start_tls` | Upgrades a plain connection instead. Both off is a cleartext bind. |
| `ldap_skip_verify` | Skips certificate verification — a warning in the UI, not a default; for a directory with an internal CA nobody has exported yet. |
| `ldap_bind_dn` / `ldap_bind_password` | The service account kubemg searches as. Empty DN means an anonymous search; a bind DN with no password is refused (`"the LDAP bind DN has no password configured"`). |
| `ldap_base_dn` | Required. Where the user search starts. |
| `ldap_user_filter` | `%s` is replaced with the escaped username. A filter with no `%s` is treated as a *restriction* and ANDed with the username lookup (`(&{filter}({user_attribute}={username}))`) rather than replacing it — useful for "only enabled accounts in this OU". |
| `ldap_user_attribute` | Default `uid`. |
| `ldap_email_attribute` | Default `mail`. |
| `ldap_group_attribute` | Default `memberOf` — the attribute on the user entry listing group DNs. Works for Active Directory and most modern OpenLDAP. |
| `ldap_group_filter` / `ldap_group_base_dn` | The fallback for a directory that keeps membership on the group entry instead (plain OpenLDAP `groupOfNames`) — `%s` is the user's DN. Only used when `ldap_group_attribute` on the user entry comes back empty. |
| `ldap_group_name_attribute` | What a matched group is called for mapping purposes — `cn` for a readable name, or empty to match on the full DN. |

Usernames and DNs are escaped against LDAP filter injection — nothing typed into the login form reaches the filter unescaped. A filter matching more than one entry is a hard error (`"the user filter matched N entries; it must match one"`) rather than picking the first match. A filter matching zero entries is reported as invalid credentials, not "no such user" — telling an unauthenticated caller which usernames exist is the one thing a login form must not do.

=== "Active Directory"

    `ldap_use_tls: true`, port 636 (or `ldap_start_tls` on 389 if your AD does not have LDAPS enabled). `ldap_user_attribute: sAMAccountName` (AD's actual login attribute — the field default `uid` will not match). `ldap_group_attribute: memberOf` (the default) reads groups straight off the user entry. `ldap_group_name_attribute: cn` reduces a group DN like `CN=platform-admins,OU=Groups,DC=example,DC=com` down to `platform-admins` for mapping patterns.

=== "OpenLDAP with memberOf overlay"

    Same shape as AD: `ldap_group_attribute: memberOf` if the overlay is enabled. Without it, leave `ldap_group_attribute` unset (it will find nothing) and configure `ldap_group_filter` such as `(&(objectClass=groupOfNames)(member=%s))` with `ldap_group_base_dn` pointing at the groups subtree, and `ldap_group_name_attribute: cn`.

!!! info "Screenshot pending — `sso-provider-form.png`"
    An OIDC provider being configured, beside the group-mapping editor.

## First-login provisioning

**Just-in-time provisioning** (on by default) creates an account the first time a directory vouches for someone kubemg has never seen, with the system role set to the provider's `default_system_role`. With it off, a directory can authenticate someone perfectly and kubemg still refuses, which is what an install that pre-creates every account via `POST /api/v1/users` wants.

Matching an existing account: kubemg looks up the identity's `ExternalID` against `(sso_provider_id, external_id)` first, falling back to matching on username. A **local account, or one owned by a different provider, with the same username is never adopted** — `ErrSSOAccountConflict` — because silently attaching a directory to an existing login would let an IdP administrator take over any kubemg account by creating a matching username. Linking the two is a deliberate act done in the user editor, not something that happens implicitly at login.

The whole sync — provisioning, group reconciliation, grant reconciliation, system role — runs in one transaction per login, because a half-applied federation (an account that exists with none of its groups) is indistinguishable from one whose access was deliberately revoked.

## Group mappings

A mapping rule (`SSOGroupMapping`) says what one external group is worth. A rule can do any combination of three things:

1. **Put the person in a local group** (`target_group_id`) — which then carries whatever that group is granted through the [permission matrix](users-and-groups.md#the-permission-matrix).
2. **Grant a Kubernetes role directly** across every cluster in an environment (`target_k8s_role`: `view`/`edit`/`cluster-admin`, optionally narrowed by `environment_filter`: `prod`/`staging`/`dev`, and optionally scoped to `namespaces`). This is the shape "everyone in `ops-oncall` gets `edit` on staging" actually takes, and it is why the environment filter exists — a rule written per cluster would need rewriting every time a cluster is registered, which is exactly when nobody remembers to.
3. **Elevate the account's system role** (`target_system_role`: `user` or `admin`, never `superadmin`) — how an IdP group becomes a kubemg administrator. A rule is only authoritative here when it actually names a role; if no matched rule mentions a system role, the stored one is left alone, so an administrator promoted by hand is not demoted at their own next sign-in. A super admin's role is never touched by any rule.

A rule needs at least one of the three, or it is refused at write time (`"a rule must grant a local group, a Kubernetes role, or a system role"`).

### Pattern matching

`external_group_pattern` matches case-insensitively, with `*` standing for any run of characters — a plain glob rather than a regular expression, because a directory's group names arrive as bare words as often as as full distinguished names, and a regular expression's metacharacters would make the obvious pattern silently match nothing. `*` alone matches every group the provider asserted, and a rule pattern of exactly `*` also matches someone the directory returned **no** groups for at all — it is treated as "about the provider" rather than "about a group."

### How mappings are evaluated (the reconcile)

On every login, kubemg evaluates every rule for that provider against the asserted groups and **reconciles** — not inserts — what they produce:

- **Local group memberships**: every row with `source = 'sso'` for this user that the matched rules no longer produce is deleted; every one they do produce is upserted. A membership added by hand (`source = 'local'`) is never touched.
- **Cluster grants**: the same reconcile, on rows with `source = 'sso'`. Several rules naming the same cluster are merged the same way a direct grant and an inherited one are — the stronger role wins, namespace scopes union. A cluster where an administrator granted access by hand (`source = 'local'`) is **left alone entirely** — federation never overwrites or reconciles away a hand-written grant, because doing so would mean the sync silently undid a deliberate decision on some later login. A **JIT elevation** (`source = 'jit'`) is treated the same way for a different reason: it is temporary and has its own clock, so reading it as "hand-written" and skipping the cluster would prune the federated grant beside it and leave the person with *less* access than they had, until the elevation expired.

Deleting a provider or a mapping rule does not retroactively revoke anything by itself — what it granted unwinds at the affected accounts' next sign-in through that provider, which is when the rules are evaluated. Revoking a session immediately as well means disabling the account.

## Testing a provider

`POST /api/v1/admin/sso/providers/:id/check` proves the stored configuration actually reaches the directory, the same idea as the cluster and datasource probes — an operator finds out here, not from the first person who cannot sign in:

- **OIDC** re-runs discovery and confirms the issuer's document names an authorization and a token endpoint.
- **SAML** reads the IdP metadata (fetching it fresh, bypassing the cache) and reports the sign-on URL and how many signing certificates it found.
- **LDAP** dials the directory, performs the service bind, and runs a base-DN search to prove it is both reachable and readable.

The result (`status`, `message`) is recorded on the provider row (`last_status`, `last_message`, `last_checked_at`) and shown in the admin list.

## Account enumeration

A federated account has no usable local password at all. `POST /api/v1/auth/login` (the plain username/password endpoint) checks for this **after** looking the username up but **before** checking any password against it, and answers identically to an unknown username or a wrong password on a local account — `401 invalid credentials`, with a dummy bcrypt comparison run regardless so the branch takes the same time either way:

A machine account gets the identical answer for a different reason — it never signs in with a password at all. Both together mean a caller probing `/auth/login` cannot distinguish "no such user" from "that user exists but signs in through SSO" from "that's a machine account."

## Troubleshooting

**Clock skew.** OIDC's ID token validity window and SAML's assertion `NotBefore`/`NotOnOrAfter` are both checked against wall-clock time. A SAML sign-in that fails (`"the SAML assertion is expired or not yet valid"`) on an otherwise-correct configuration is almost always the bastion host and the IdP disagreeing about the time — check NTP on both sides before touching the provider configuration.

**Wrong callback / redirect URI.** The single most common failure. The redirect URI (OIDC) or ACS URL (SAML) registered at the IdP must match what kubemg computes byte-for-byte: `{public_url}/api/v1/auth/sso/providers/{id}/callback`. If `KUBEMG_PUBLIC_URL` (or the runtime override in Settings) changes, every provider's registered URI at its IdP has to be updated to match — kubemg does not tell the IdP anything, it only computes and displays what has to be registered there.

**A SAML sign-in fails at the audience check.** `"the SAML assertion was issued for a different service"` means the entity ID kubemg is sending as its own does not match what the IdP has registered for this relying party — check `saml_entity_id` (or the generated default, the metadata URL) against what was configured at the IdP, and re-upload kubemg's SP metadata if the entity ID changed.

**Missing claims or attributes.** If `username_claim`/`email_claim`/`groups_claim` are left blank and sign-in reports `"the ID token carries no username claim"` or `"the SAML assertion carries no username"`, the IdP is not sending any of the fallback claim names kubemg tries. Set the claim name explicitly to whatever the IdP is actually configured to emit, or add a claim/attribute mapping at the IdP.

**Groups claim not present.** Most commonly an OIDC provider that keeps group membership out of the ID token; kubemg already falls back to the UserInfo endpoint when the ID token carries no groups, but if UserInfo does not carry them either (Google Workspace without a directory sync layer, some default Okta/Entra configurations), no client-side retry will produce them — the mapping has to either use a `*` pattern rule for a baseline grant, or the IdP configuration has to be changed to emit the claim (a scope, a claims mapping policy, or a directory sync product).

**TLS to the IdP.** For LDAP, `ldap_skip_verify` is the escape hatch for an internal CA nobody has exported into kubemg's trust store yet — it is surfaced as a standing warning in the console rather than a default, and should be turned off once the CA is trusted properly. For OIDC discovery and SAML metadata fetches over plain HTTP, the failure surfaces as a fetch error at `check` time rather than at someone's sign-in — always run the check after saving a provider.

## The REST routes

??? note "Administrative routes — `/api/v1/admin/sso`, admin session required"

    ```
    GET    /api/v1/admin/sso/providers
    POST   /api/v1/admin/sso/providers
    PUT    /api/v1/admin/sso/providers/:id
    DELETE /api/v1/admin/sso/providers/:id
    POST   /api/v1/admin/sso/providers/:id/check
    GET    /api/v1/admin/sso/mappings
    POST   /api/v1/admin/sso/mappings
    PUT    /api/v1/admin/sso/mappings/:id
    DELETE /api/v1/admin/sso/mappings/:id
    ```

??? note "Public routes the login page uses — `/api/v1/auth/sso/providers`"

    ```
    GET  /api/v1/auth/sso/providers              # enabled providers, name + protocol only
    GET  /api/v1/auth/sso/providers/:id/login    # OIDC/SAML: redirects to the IdP
    POST /api/v1/auth/sso/providers/:id/login    # LDAP: username + password, kubemg's own form
    GET  /api/v1/auth/sso/providers/:id/callback # OIDC callback (GET, ?code=...)
    POST /api/v1/auth/sso/providers/:id/callback # SAML callback (POST, SAMLResponse)
    GET  /api/v1/auth/sso/providers/:id/metadata # kubemg's own SAML SP metadata
    ```

The full surface, with payloads, is in the developer guide's
[REST API reference](../dev/api.md#federated-sign-in-sso).
