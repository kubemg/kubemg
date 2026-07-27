# Task: Implement Rancher-Style 3rd Resource Navigation Sidebar on Explore Page

## Context & Objective
In KubeMG, the Explore page (`/explore`) needs a **3rd Resource Navigation Sidebar** (similar to Rancher's resource tree menu) to allow cluster operators and developers to browse Kubernetes resources grouped by logical categories.

---

## Technical Instructions for Claude-CLI

### 1. Backend (`backend/pkg/api/`)
- Update `resources.go` and `router.go` to add or expand listing handlers for the following resource categories over the agent tunnel (via `s.proxy.Call` with impersonation headers):
  - **Workloads**: `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs`, `cronjobs`.
  - **Networking**: `services`, `ingresses`, `httproutes` (gateway.networking.k8s.io), `virtualservices` (networking.istio.io). Handle missing CRD (404) gracefully by returning an empty list or appropriate status.
  - **Storage & Config**: `persistentvolumes` (PV - cluster-scoped), `persistentvolumeclaims` (PVC), `storageclasses` (cluster-scoped), `configmaps`, `secrets` (expose metadata only, omit raw secret data).
  - **Custom Resources**: `customresourcedefinitions` (CRDs).
  - **Cluster**: `nodes` (cluster-scoped), `namespaces`.
- Ensure namespace-scoped queries apply namespace isolation based on user's granted permissions (`resourceNamespace(c, grant)`).

### 2. Frontend (`frontend/src/`)
- **API Client (`api/types.ts` & `api/client.ts`)**:
  - Add TypeScript interfaces and API methods to fetch services, ingresses, configmaps, secrets, PVs, PVCs, storageclasses, nodes, and CRDs.
- **3rd Sidebar Component (`components/ExploreSidebar.tsx`)**:
  - Create a collapsible resource menu sidebar with categories:
    - 📦 **Workloads**: Pods, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs
    - 🌐 **Networking**: Services, Ingresses, HTTPRoutes, VirtualServices
    - 💾 **Storage & Config**: PVs, PVCs, StorageClasses, ConfigMaps, Secrets
    - 🧩 **Custom Resources**: CRDs
    - 🖥️ **Cluster**: Nodes, Namespaces
  - Include search/filter input to quickly filter resources in the sidebar.
  - Follow Signal Deck design tokens (`bg-rail`, `bg-rail-raised`, `text-accent`, etc.).
- **Explore Page (`pages/Explore.tsx`)**:
  - Render `ExploreSidebar` on the left side of the Explore content view (forming the 3rd navigation level in KubeMG layout: Level 1 Rail -> Level 2 Panel -> Level 3 Resource Sidebar).
  - Update main content area to display table views corresponding to the selected resource item.
  - Maintain namespace selection dropdown at the top for namespace-scoped resources and disable/hide for cluster-scoped resources (Nodes, PVs, StorageClasses, CRDs).

---

## Verification Rules
1. Run containerized verification & linting:
   ```bash
   make verify
   ```
2. Run containerized test suite:
   ```bash
   make test
   ```
3. Update `roadmap.md` to check off (`[x]`) completed items upon success.
