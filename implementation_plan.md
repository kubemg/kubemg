# Implementation Plan - Phase 2: Bastion Architecture, Dumb Agent & Cluster Registration Wizard

This implementation plan details the architecture, file structures, API endpoints, Kustomize deployment manifests, agent binary, and verification steps for **Phase 2: Bastion Architecture & Dumb Agent**.

---

## User Review Required

> [!IMPORTANT]
> - All development, builds, tests, and verifications MUST run inside Docker containers using `make verify`.
> - Code modifications will be executed strictly by **Claude-CLI**.
> - Antigravity (Planner) only updates planning artifacts (`implementation_plan.md`, `claude_prompt.md`, `roadmap.md`).

---

## Architecture Overview

```
 [ Dev / Admin ]
        │  (kubectl / Web UI)
        ▼
┌─────────────────────────────────────────────────────────────┐
│ KubeMG Central Server (Port 8080 / 443)                      │
│  ├─ Web UI (Cluster Wizard, User/Group IAM, Kubeconfig)     │
│  ├─ Auth & Permission Matrix Engine                         │
│  ├─ Kustomize Manifest Generator API                        │
│  ├─ Audit Logging Engine                                    │
│  └─ Bastion Tunnel Listener (gRPC / WebSocket Pool)         │
└─────────────────────────────▲───────────────────────────────┘
                              │
                    Outbound Reverse Tunnel
                     (Port 443 / gRPC)
                              │
┌─────────────────────────────┴───────────────────────────────┐
│ Target Kubernetes Cluster                                    │
│  └─ Dumb Agent (10-15MB Go Binary)                          │
│      ├─ ServiceAccount + RBAC                               │
│      └─ Proxies to https://kubernetes.default.svc           │
└─────────────────────────────────────────────────────────────┘
```

---

## Proposed Changes

### 1. Cluster Registration Wizard UI

#### [NEW] [ClusterWizard.tsx](file:///home/bastion/Desktop/kubemg/frontend/src/pages/ClusterWizard.tsx)
- Step 1: **Cluster Information** (Name, Environment: `prod`/`staging`/`dev`, Description).
- Step 2: **Connection Mode Selection**:
  - **Agent-Based (Recommended)**: Generates one-line `kubectl apply -k <kustomize-url>` command snippet & downloadable YAML package containing registration token secret.
  - **Direct K8s API**: Input API server URL, CA certificate bundle, Service Account Token.
- Step 3: **Connection Health Check**: Live polling backend status check (`pending` -> `healthy`).
- Step 4: **Access & RBAC Assignment**: Assign initial permissions to users/groups.

#### [MODIFY] [App.tsx](file:///home/bastion/Desktop/kubemg/frontend/src/App.tsx)
- Add route `/clusters/new` pointing to `ClusterWizard.tsx`.
- Update Cluster Management view (`ClusterManagement.tsx`) button to redirect to `/clusters/new`.

---

### 2. Kustomize Agent Deployment Package & Manifest API

#### [NEW] [deploy/kustomize/base/](file:///home/bastion/Desktop/kubemg/deploy/kustomize/base/)
- `kustomization.yaml`: Base Kustomize configuration.
- `deployment.yaml`: Deployment spec for `kubemg-agent` (resource limits: 32MB RAM, 50m CPU).
- `rbac.yaml`: ServiceAccount, ClusterRole, ClusterRoleBinding for API proxying & impersonation.
- `secret.yaml`: Placeholder secret for `AGENT_REGISTRATION_TOKEN` and `BASTION_URL`.

#### [MODIFY] [routes.go / kustomize.go](file:///home/bastion/Desktop/kubemg/backend/pkg/api/)
- Add endpoint `GET /api/v1/clusters/:id/kustomize`:
  - Dynamically builds and serves valid Kustomize package (or single YAML manifest) pre-configured with the cluster's unique registration token and central Bastion URL.

---

### 3. Open-Source "Dumb Agent" (`agent/`)

#### [NEW] [agent/cmd/agent/main.go](file:///home/bastion/Desktop/kubemg/agent/cmd/agent/main.go)
- Lightweight Go application (10-15MB compiled).
- Reads `BASTION_URL` and `CLUSTER_TOKEN` from environment/secrets.
- Establishes outbound TLS gRPC / WebSocket stream to central KubeMG Bastion server.
- Handles incoming proxy requests from central Bastion stream and forwards them to in-cluster K8s API (`https://kubernetes.default.svc`).

#### [NEW] [agent/go.mod](file:///home/bastion/Desktop/kubemg/agent/go.mod) & [agent/Dockerfile](file:///home/bastion/Desktop/kubemg/agent/Dockerfile)
- Multi-stage Dockerfile compiling lightweight static Go binary (`CGO_ENABLED=0`).

---

### 4. Central Bastion Proxy Server & Audit Logging

#### [NEW] [backend/pkg/bastion/](file:///home/bastion/Desktop/kubemg/backend/pkg/bastion/)
- `server.go`: Bastion tunnel listener handling incoming agent connections, token authentication, and managing active cluster connection pool.
- `proxy.go`: Intercepts `kubectl` API requests, attaches `Impersonate-User` and `Impersonate-Group` HTTP headers, and forwards requests to target cluster agent.
- `audit.go`: Logs formatted audit records (`timestamp`, `user_id`, `username`, `cluster_id`, `verb`, `uri`, `status_code`).

---

## Verification Plan

### Automated Verification
- Run `make verify` inside Docker container environment:
  1. Backend & Bastion packages compile cleanly (`go build ./...`).
  2. Agent binary compiles static executable (`go build -o agent ./agent/cmd/agent`).
  3. Frontend compiles without TypeScript errors (`npm run build`).
  4. Docker Compose & CI pipeline pass cleanly.

### Manual Verification
- Access `/clusters/new` wizard UI, register test cluster metadata, and select "Agent-Based" installation mode.
- Copy generated Kustomize command snippet (`kubectl apply -k ...`).
- Verify agent connection status transitions to `healthy` upon successful tunnel handshake.
