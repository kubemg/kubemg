# KubeMG Roadmap

This document tracks the high-level phases and granular tasks for the KubeMG project.
- `[ ]` Pending
- `[/]` In Progress
- `[x]` Completed

## Phase 1: MVP (Advanced Local Management & Multi-Cluster Support)
- [x] Initialize core project structure (Backend & Frontend)
- [x] Define backend technology stack (Go or Rust) and web framework (React/Next.js or Vue)
- [x] Create Docker Compose environment for local development (Backend & Frontend services)
- [x] Containerized build/test/lint pipeline (`Makefile` + `docker-compose.ci.yml`, no host toolchain)
- [x] Implement local user database and authentication (DevOps users)
- [x] Implement Multi-Cluster management database schema and API endpoints (Cluster registration & permissions)
- [x] Integrate K8s TokenRequest API to generate cluster-specific short-lived `kubeconfig` files
- [x] Design and implement UI for cluster selector, cluster management, and downloading cluster-specific `kubeconfig`
- [x] Implement Advanced Local User & Group Management engine (User CRUD, Local Groups, active/disabled status, User-Group mappings)
- [x] Implement UI for User & Group Administration and Cluster Access Permission Matrix

## Phase 2: Bastion Architecture & Dumb Agent
- [x] Refactor Cluster Registration flow into a dedicated step-by-step Wizard UI (`/clusters/new`)
- [x] Create Kustomize manifest generator & endpoint for one-step agent deployment (`kubectl apply -k ...`)
- [x] Develop the central Bastion/Proxy server (gRPC / WebSocket reverse tunnel listener)
- [x] Develop the lightweight open-source "Dumb Agent" client (`agent/`) for reverse tunnel connection
- [x] Implement `kubectl` API traffic proxying, K8s Impersonation headers (`Impersonate-User`, `Impersonate-Group`), and audit logging

Carried into Phase 3 and completed there:
- [x] Stream `exec`, `attach`, `watch` and `logs -f` over the tunnel (protocol v2)
- [x] Persist audit records to a queryable store and surface them in the UI

## Phase 3: Single Pane of Glass UI & Observability
- [x] Develop UI for multi-cluster namespace and resource visibility (RBAC-aware)
- [x] Implement on-demand state fetching via Dumb Agent
- [x] Settings page: server URL, agent image and agent namespace configurable at runtime instead of only through the environment
- [x] Rebuild the console UI on the Signal Deck design system: two-level rail with a live fleet list, ⌘K command palette, dark/light decks, self-hosted Inter + JetBrains Mono, and the link strand as the state device
- [x] Add Rancher-style 3rd resource navigation sidebar to Explore page (Workloads, Networking, Storage & Config, Custom Resources, Cluster)
- [ ] Integrate VictoriaMetrics for minimal footprint metrics
- [ ] Integrate VictoriaLogs/Promtail for minimal footprint logs

Still open from the streaming work:
- [ ] `port-forward` over the tunnel — refused with `501` today; it multiplexes arbitrary TCP inside one session and needs its own framing
- [ ] `kubectl exec` against a plaintext bastion: client-go refuses to send bearer tokens over `http://`, so kubectl needs TLS in front of the bastion (the browser terminal is unaffected)
- [ ] Audit retention policy — `PruneAuditEvents` exists but nothing calls it on a schedule

## Phase 4: Enterprise SSO & Identity Provider Federation
- [ ] Implement SAML/OIDC/LDAP integration module
- [ ] Implement IdP group federation mapping logic to local groups and K8s RoleBindings
