# Task: Implement Audit & Session Recording Retention, Filtering, SIEM Webhooks & Security Alarms

Refer to `implementation_plan.md` and `PROJECT_KNOWLEDGE.md` before starting. Follow `agentrule.md` strictly (run builds and tests via Docker using `make verify` / `make test`).

## Key Tasks to Implement:

1. **Audit & Session Recording Retention Policies (Settings UI & Backend Pruner)**:
   - Update `backend/pkg/db/settings.go` & `models.go` to add `SessionRecordingRetentionDays` (default 30) alongside `AuditRetentionDays`.
   - Update `backend/pkg/api/settings.go` and `frontend/src/pages/Settings.tsx` to allow admins to set audit and session recording retention periods.
   - Extend `backend/pkg/api/audit_prune.go` background pass (running every 12h) to prune audit DB records past `audit_retention_days` and delete expired `terminal_sessions` metadata and `.cast` files past `session_recording_retention_days`.

2. **Audit Date & Time Range Filtering**:
   - Update `backend/pkg/api/audit.go` and `backend/pkg/db/audit.go` to support `from` and `to` query parameters (`created_at >= ? AND created_at <= ?`).
   - Update `frontend/src/pages/Audit.tsx` to add Date & Time picker controls (`Start Time`, `End Time`) and quick preset buttons (`Last 1 Hour`, `Last 24 Hours`, `Last 7 Days`, `All Time`).

3. **External SIEM Webhook Exporter Engine**:
   - Create `backend/pkg/db/webhooks.go` & `webhook_models.go` with `audit_webhooks` table (`id`, `name`, `target_url`, `headers_json`, `format`, `enabled`).
   - Implement asynchronous worker queue `backend/pkg/audit/exporter.go` to stream audit log events to external aggregators (Elasticsearch NDJSON format, ClickHouse JSONEachRow, QRadar/Generic Webhook).
   - Implement `backend/pkg/api/audit_webhooks.go` REST endpoints and `frontend/src/components/AuditWebhookSettings.tsx` configuration panel.

4. **Denied Operation Security Alarms & Alerts**:
   - Create `backend/pkg/db/alarms.go` & `alarm_models.go` for `security_alert_rules` table (`status_code == 403` / `action == Denied`, threshold, notification target URL).
   - Implement real-time trigger in `backend/pkg/audit/alarm_engine.go` (intercepting audit logs in `pkg/bastion/storeaudit.go`) to send alerts (Slack/Webhook) when unauthorized/denied operations occur.
   - Implement API endpoints `backend/pkg/api/security_alarms.go` and `frontend/src/components/SecurityAlarmsManager.tsx` UI.

5. **Verification & Roadmap Update**:
   - Run `make verify` inside Docker container environment (`make verify` / `make test`) to ensure all tests and builds pass.
   - Update `roadmap.md` to check off completed items under Phase 5.
