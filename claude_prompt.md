# Task: Implement VictoriaMetrics & VictoriaLogs Query Engines (Phase 3 Remaining Items)

Codebase Audit Note:
- **Universal Resource Detail View & Describe Engine** is already **COMPLETED** (`ResourceDetailDrawer.tsx`). Do NOT touch or re-implement it.
- **Observability Datasource Registration & Discovery** is already **COMPLETED** (`DatasourcePanel.tsx`, `backend/pkg/observability/`).

Your task is to implement the **Query Paths** for historical metrics and aggregated log search:

## 1. VictoriaMetrics Historical Metrics Query Engine (Query Path)
- Create `backend/pkg/observability/metrics_query.go` to proxy PromQL queries (`/api/v1/query_range`, `/api/v1/query`) to the active `metrics` datasource using `Target.requestPath(...)`.
- Add endpoint `GET /api/v1/clusters/:id/observability/metrics/query` in `backend/pkg/api/observability.go`.
- Create `frontend/src/components/MetricsChart.tsx` for responsive CPU/Memory time-series line charts.
- Embed `MetricsChart` into `PodPanels.tsx` and cluster overview when a `metrics` datasource is active.

## 2. VictoriaLogs / Loki Aggregated Logs Query Engine (Query Path)
- Create `backend/pkg/observability/logs_query.go` to proxy LogSQL/LogQL queries to the active `logs` datasource using `Target.requestPath(...)`.
- Add endpoint `GET /api/v1/clusters/:id/observability/logs/query` in `backend/pkg/api/observability.go`.
- Create `frontend/src/components/LogExplorer.tsx` to search and filter historic/multi-pod logs.

## 3. Verification & Roadmap Update
- Run `make verify` and `make test` inside Docker.
- Open `roadmap.md` and check off completed Phase 3 items:
  - `- [x] Universal Resource Detail View & Describe Engine...`
  - `- [x] Integrate VictoriaMetrics for minimal footprint metrics...`
  - `- [x] Integrate VictoriaLogs/Promtail for minimal footprint logs...`
