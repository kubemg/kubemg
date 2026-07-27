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
- [x] Live utilisation from the cluster's own Metrics API (`/metrics/nodes`, `/metrics/pods`), surfaced as capacity meters on the fleet, the cluster page and the pod drawer
- [x] Log viewer controls on the streamed container log: line filter, wrap toggle, tail toggle
- [x] Resource YAML viewer and live editor — `GET|PUT /clusters/:id/resources/object` reads and replaces one object through the same impersonated tunnel, so the cluster's own RBAC decides whether a write lands and the change is audited as an `update`. Every Explore list carries per-row *View YAML* / *Edit config* actions opening a highlighted editor in the shared `Sheet`. Server-side bookkeeping (managed fields, kubectl's last-applied copy) is stripped, and a Secret is shown redacted and refused as a write — the manifest is not the whole object, so applying it would overwrite every value with its placeholder
- [x] Shell selector (`bash` / `sh`) on the pod terminal. Kubernetes takes `command` as an argv rather than a candidate list, so the previous pair of `command` parameters ran `/bin/bash /bin/sh` instead of falling back; exactly one shell is now sent, changing it opens a fresh session, and "executable file not found" names the picker as the fix
- [ ] Resource YAML Viewer & Live Editor: Add UI controls to view reachable K8s resources (Deployments, Pods, Services, etc.) in YAML format and configure/update them live
- [ ] Multi-Shell Exec Terminal: Support both `sh` and `bash` shell options for Pod container interactive terminal sessions
- [ ] Integrate VictoriaMetrics for minimal footprint metrics — **not started.** The Metrics API above answers "what is this using right now"; it is a two-minute sliding window with no history, so a series backend is still needed for anything over time
- [ ] Integrate VictoriaLogs/Promtail for minimal footprint logs — **not started.** Logs today are read live from the pod through the tunnel, so nothing survives a pod restart and nothing is searchable across pods

Still open from the streaming work:
- [x] `port-forward` over the tunnel — carried in its WebSocket transport (`v2.portforward.k8s.io`), which the existing upgrade bridge multiplexes verbatim. The SPDY transport is still refused with a `501` that names the fix; implementing it would mean a second multiplexing protocol inside the tunnel for a transport Kubernetes is retiring
- [ ] `kubectl exec` against a plaintext bastion: client-go refuses to send bearer tokens over `http://`, so kubectl needs TLS in front of the bastion (the browser terminal is unaffected)
- [x] Audit retention policy — a background pass every 12 hours prunes past `audit_retention_days`, configurable from the Settings page

## Phase 4: Enterprise SSO & Identity Provider Federation
- [ ] Implement SAML/OIDC/LDAP integration module
- [ ] Implement IdP group federation mapping logic to local groups and K8s RoleBindings
