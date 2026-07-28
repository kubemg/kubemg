# Task: Implement Phase 4 - Enterprise SSO & Identity Provider Federation

Refer to `implementation_plan.md` and `PROJECT_KNOWLEDGE.md` before starting. Follow `agentrule.md` strictly (run builds and tests via Docker using `make verify` / `make test`).

## Key Tasks to Implement:

1. **Database Schema & Models (`backend/pkg/db/sso_models.go`, `store.go`)**:
   - Create `SSOProviderConfig` model (`protocol`: `oidc`|`saml`|`ldap`, `name`, `client_id`, `client_secret`, `issuer_url`, `saml_metadata_url`, `ldap_host`, etc.).
   - Create `SSOGroupMapping` model (`external_group_pattern`, `target_group_id`, `target_k8s_role`, `environment_filter`).
   - Add `AuthSource` and `ExternalID` fields to `User` model.
   - Implement `SyncSSOUserAndGroups` in `store.go` for JIT provisioning and group synchronization.

2. **Auth Engines (`backend/pkg/auth/`)**:
   - Create `oidc.go`: OIDC Discovery, PKCE authorization URL generation, code exchange, and JWT/ID-token verification.
   - Create `saml.go`: SAML 2.0 SP metadata generator, AuthnRequest, and ACS assertion parser.
   - Create `ldap.go`: LDAP bind authenticator and group membership query engine (`memberOf`).

3. **API Endpoints (`backend/pkg/api/sso.go`, `routes.go`)**:
   - `GET /api/v1/auth/sso/providers`: Public list of active SSO providers.
   - `GET /api/v1/auth/sso/:id/login`: Redirect to IdP.
   - `GET/POST /api/v1/auth/sso/:id/callback`: Process callback, sync user/groups, issue KubeMG JWT.
   - `GET|PUT|DELETE /api/v1/admin/sso/providers` and `/mappings`: Admin management endpoints.

4. **Frontend UI (`frontend/src/components/`)**:
   - Create `SsoLoginPage.tsx`: SSO login buttons on authentication page.
   - Create `SsoSettingsPanel.tsx`: Admin panel for configuring OIDC, SAML, and LDAP providers.
   - Create `GroupMappingEditor.tsx`: Rule editor for mapping IdP groups to local groups & K8s roles.

5. **Verification & Cleanup**:
   - Run `make verify` and `make test` inside Docker.
   - Update `roadmap.md` to check off completed Phase 4 items:
     - `- [x] Implement SAML/OIDC/LDAP integration module`
     - `- [x] Implement IdP group federation mapping logic to local groups and K8s RoleBindings`
