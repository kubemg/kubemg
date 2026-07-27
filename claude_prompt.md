Read `implementation_plan.md` and `roadmap.md` carefully.

Execute Phase 2: Bastion Architecture, Dumb Agent & Cluster Registration Wizard.

Tasks to implement:

1. Refactor Cluster Onboarding UI into a Dedicated Wizard Page (`frontend/src/`):
   - Create `frontend/src/pages/ClusterWizard.tsx` with a multi-step workflow:
     - Step 1: Cluster Metadata (Name, Environment: `prod`/`staging`/`dev`, Description).
     - Step 2: Connection Mode & Agent Installer (Agent-based vs Direct K8s API token). Display copyable `kubectl apply -k <kustomize-url>` snippet & registration token.
     - Step 3: Healthcheck Polling (Verify status transition from `pending` -> `healthy`).
     - Step 4: Initial Access & RBAC Assignment.
   - Register `/clusters/new` route in `App.tsx` and link it from Cluster Management button.

2. Kustomize Agent Manifest Generator (`deploy/kustomize/` & `backend/pkg/api/`):
   - Add base Kustomize templates in `deploy/kustomize/base/`: `kustomization.yaml`, `deployment.yaml` (resource-capped agent pod), `rbac.yaml` (ServiceAccount & ClusterRoleBinding), `secret.yaml`.
   - Add API handler `GET /api/v1/clusters/:id/kustomize` serving dynamically assembled Kustomize package (injecting cluster registration secret token & Bastion URL).

3. Lightweight Open-Source "Dumb Agent" (`agent/`):
   - Create Go client in `agent/cmd/agent/main.go` that connects to central Bastion URL via outbound gRPC/WebSocket stream.
   - Proxies incoming tunnel requests to local K8s API server (`https://kubernetes.default.svc`).
   - Create `agent/Dockerfile` for minimal static binary build (`CGO_ENABLED=0`).

4. Central Bastion Proxy Server & Audit Engine (`backend/pkg/bastion/`):
   - `server.go`: Bastion tunnel listener handling incoming agent connections & cluster token handshakes.
   - `proxy.go`: Proxy handler for `kubectl` requests attaching `Impersonate-User` and `Impersonate-Group` headers.
   - `audit.go`: Structured audit logger for all proxied API actions.

5. Verification:
   - Run `make verify` inside Docker container environment to validate backend, agent, and frontend builds, tests, and linters.

6. Update `roadmap.md`:
   - Mark Phase 2 items as completed (`[x]`) upon successful verification.
