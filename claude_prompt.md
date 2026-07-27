# Phase 3 Finalization Task Prompt for Claude-CLI

## Objective
Finalize Phase 3 of KubeMG by implementing:
1. Observability metrics & log viewer integration (VictoriaMetrics / K8s Metrics API & VictoriaLogs / Log stream filtering).
2. Automated background audit event retention pruner.
3. Tunnel `port-forward` TCP multiplexing support over WebSocket reverse proxy tunnel.

---

## Technical Implementation Tasks

### 1. Backend (`backend/pkg/`)
- **Audit Retention Scheduler (`pkg/api/` & `pkg/db/`)**:
  - Add a background scheduler/ticker in `backend/pkg/api/` (e.g. `startAuditPruner(ctx)`) running every 12 hours.
  - Read `audit_retention_days` setting from `db.Store` (defaulting to 30 days) and call `s.store.PruneAuditEvents(ctx, cutoff)`.
  - Expose `audit_retention_days` in the `/api/v1/settings` API payload and handler.
- **Metrics API Integration (`pkg/api/metrics.go`)**:
  - Add `/api/v1/clusters/:id/metrics/pods` and `/api/v1/clusters/:id/metrics/nodes` API handlers.
  - Proxy requests over the agent tunnel to `/apis/metrics.k8s.io/v1beta1/pods` and `/apis/metrics.k8s.io/v1beta1/nodes` (or VictoriaMetrics backend endpoint if present). Return normalized JSON structure for pod/node CPU (mcores) and Memory (MiB/GiB).
- **Port-Forward Multiplexing (`pkg/bastion/tunnel.go` & `pkg/api/`)**:
  - Upgrade WebSocket agent proxy handler to support `port-forward` requests (`/api/v1/namespaces/:ns/pods/:pod/portforward`), implementing TCP stream framing over the tunnel session.

### 2. Frontend (`frontend/src/`)
- **Settings Page (`pages/Settings.tsx`)**:
  - Add configuration field for "Audit Event Retention (Days)" allowing administrators to update retention setting.
- **Pod & Node Metrics UI (`components/PodDrawer.tsx`, `pages/Explore.tsx`, `pages/Overview.tsx`)**:
  - In `PodDrawer`: Display CPU & Memory utilization progress bars and real-time values for running pods.
  - In `Overview` & `ClusterDetail`: Display Node CPU/Memory usage summaries and cluster metrics health.
- **Log Viewer Enhancements (`components/PodDrawer.tsx`)**:
  - Add live log search filter box, line wrap toggle, and auto-scroll control to the container log stream drawer.

---

## Verification Rules
1. Run containerized verification & linter:
   ```bash
   make verify
   ```
2. Run containerized test suite:
   ```bash
   make test
   ```
3. Open `roadmap.md` and check off (`[x]`) all completed Phase 3 items upon successful verification.
