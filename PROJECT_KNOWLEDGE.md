# KubeMG - Modern K8s Access and Management Platform

## 1. Project Purpose and Vision
KubeMG is a lightweight, web-based, centralized Kubernetes multi-cluster management platform built as a clean, performant alternative to heavy platforms like Rancher, Lens, or K8s Dashboard. Its core goal is central cluster administration, cluster health visibility, and unified RBAC access management across multiple Kubernetes clusters, with self-service short-lived `kubeconfig` generation serving as one of its key security features.

## 2. Core Pain Points Addressed
*   **Heavy Agent Problem:** Tools like Rancher deploy heavy agents and dozens of CRDs into the cluster, choking the cluster and trying to take full control.
*   **Poor UX and Local Dependency:** K8s Dashboard offers a very outdated UX, while tools like Lens are desktop-dependent and lack centralized team management (SSO/RBAC).
*   **Access and Security Management (PAM):** Providing developers with restricted and secure K8s access (kubeconfig) is currently a significant operational burden and is often done using insecure methods.

## 3. Core Features and Roadmap
*   **Phase 1 - MVP (Advanced Local Management & Multi-Cluster Support):** Comprehensive local user and group management engine (User CRUD, Local Groups, status control, password policies). Multi-cluster registration and granular user/group cluster access control. UI-driven, cluster-specific short-lived `kubeconfig` generation using K8s TokenRequest API.
*   **Phase 2 - Bastion Architecture & Dumb Agent:** Central Bastion/Gateway proxying port 443 with outbound reverse tunnels (gRPC/WebSocket) from target clusters and audit logging.
*   **Phase 3 - Single Pane of Glass UI & Observability:** Modern multi-cluster resource administration dashboard with on-demand pod state and lightweight metrics/logs (VictoriaMetrics/VictoriaLogs).
*   **Phase 4 - Enterprise SSO & Identity Provider Federation:** Enterprise integration with corporate authentication systems (SAML, LDAP, OIDC) and IdP group federation mapping.

## 4. Architecture and Security Infrastructure
Unlike standard tools on the market, KubeMG is designed with a SecOps-friendly multi-tenant / multi-cluster architecture:
*   **Advanced Local User & Group Management (IAM):** KubeMG features a full-fledged built-in IAM engine for managing local users, local groups, status (active/disabled), and role assignments independently. SSO/OIDC acts as an extension layer built on top of this local foundation.
*   **Multi-Cluster Management:** KubeMG centralizes access across multiple Kubernetes clusters (on-prem, EKS, GKE, AKS, edge). Admins register clusters, and users/groups are assigned granular cluster-level permissions.
*   **Cluster-Specific Kubeconfig Generation:** When developers request access, KubeMG generates a short-lived `kubeconfig` tailored specifically to the selected target cluster using its API endpoint, CA bundle, and a scoped `TokenRequest` token.
*   **Proxy and Bastion Architecture:** Developers do not need direct network access (VPN, etc.) to the K8s API or Nodes. KubeMG acts as a Bastion/Gateway (Proxy) over port 443, taking over the traffic, inspecting all `kubectl` commands (including exec, logs), and keeping audit logs.
*   **Dumb Agent (Reverse Tunnel):** Heavy agents are not installed on target clusters. Only a 10-15 MB "dumb" proxy agent is deployed, which opens an outbound reverse tunnel to the central KubeMG server via gRPC/WebSocket. This eliminates the need to open inbound ports on the customer's firewall.
*   **K8s Impersonation:** The intermediary proxy architecture communicates with the K8s API using "Impersonation" headers (`Impersonate-User`, `Impersonate-Group`). This eliminates the complexity of managing separate Service Account tokens for each user.

## 5. Observability Strategy
To avoid overloading the Kubernetes API for metrics and logs, data sources are isolated:
*   **On-Demand State:** Live status and configurations of Pods are fetched on-demand directly from the K8s API via the lightweight "Dumb Agent".
*   **BYO or Auto-Provision Metrics/Logs:** Users are offered options to "bring your own stack" (existing Prometheus/Loki/Elastic) or "install a lightweight stack". For automatic installation, the **VictoriaMetrics** (metrics) and **VictoriaLogs/Promtail** (logs) stack is used to keep resource consumption minimal, avoiding heavy monitoring tools like Rancher's.

## 6. Distribution and Go-to-Market (GTM)
KubeMG is not an open-source project but is positioned as commercial "Enterprise Software" (Compiled/Closed-Source) where intellectual property (IP) is protected.
*   **Closed-Source Core (Intelligence):** The central backend (Go/Rust), UI, and identity/authorization management modules are compiled into machine code and distributed as closed-source with a license key (SaaS or On-Premise).
*   **Open-Source Agent:** To gain the trust of SecOps teams, the "Dumb Agent" installed on the target cluster (which solely acts as a tunnel) is open-sourced.
