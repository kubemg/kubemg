# Task: Implement Resource YAML Viewer/Editor and Pod Multi-Shell Exec Terminal

Please implement the following two features in KubeMG based on the implementation plan:

1. **Resource YAML Viewer & Live Editor:**
   - Add "View YAML" and "Edit Config" buttons to resource action menus (Deployments, Pods, Services, Ingresses, ConfigMaps, etc.).
   - Create a drawer/modal component with a syntax-highlighted YAML editor to view and live-edit Kubernetes resources.
   - Backend API endpoints to retrieve resource YAML and apply YAML updates (`PUT`/`PATCH`) via the KubeMG Bastion reverse proxy with K8s Impersonation.

2. **Pod Exec Terminal Shell Selector (`bash` / `sh`):**
   - Add shell selection capability (`/bin/bash` vs `/bin/sh`) in the Pod interactive container terminal UI.
   - Ensure the backend/tunnel exec handler forwards the chosen shell executable to the Kubernetes API exec WebSocket stream.

3. **Verification & Testing:**
   - Run all builds, tests, and verifications using `make verify` inside Docker containers.
   - Update completed tasks in `roadmap.md`.
