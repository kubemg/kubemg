# Prometheus metrics

KubeMG can expose a Prometheus scrape endpoint at `GET /metrics`. It is **off by default** because the endpoint discloses version, route inventory, and process details on the same port that agent clusters dial into from outside. Enable it only once that port is network-restricted to your scraper (firewall rule, internal load balancer, etc.).

## Enabling

Set `KUBEMG_METRICS_ENABLED=true` before starting the server:

```dotenv
KUBEMG_METRICS_ENABLED=true
```

The endpoint is then available at `/metrics` on the same address as the rest of the API.

!!! warning "Exposure risk"
    The `/metrics` endpoint shares the port `KUBEMG_PUBLIC_URL` points at — the port your agent clusters reach. It discloses the exact build version (CVE-targeting material), per-route traffic and error rates, and process details (goroutine count, heap size, open file descriptors). Do not expose it to the internet or to agent clusters without a network control in front of it.

## Metrics exposed

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `kubemg_http_requests_total` | Counter | `method`, `path`, `status` | Route pattern, not actual path — `/clusters/:id` rather than `/clusters/42`. Unmatched routes are grouped as `path="unmatched"`. WebSocket connections (tunnel, attach, exec) are counted here once the upgrade completes. |
| `kubemg_http_request_duration_seconds` | Histogram | `method`, `path` | Latency for ordinary HTTP requests. WebSocket upgrades are excluded — they hold the connection for the session lifetime and would make the histogram meaningless. |
| `kubemg_http_requests_in_flight` | Gauge | — | Ordinary HTTP requests currently being served. WebSocket connections are excluded for the same reason. |
| `kubemg_db_query_duration_seconds` | Histogram | `operation` | Per GORM operation type: `create`, `query`, `update`, `delete`, `row`, `raw`. |
| `kubemg_db_queries_total` | Counter | `operation`, `error` | `error="true"` means a real database error. `ErrRecordNotFound` is not an error — it is normal flow for existence checks. |
| `kubemg_build_info` | Gauge | `version` | Always `1`. Use the `version` label to correlate anomalies with deploys. |

Go runtime and process metrics (goroutines, GC pauses, file descriptors) come from the standard Prometheus registry and are included automatically.

## Example Prometheus scrape config

```yaml
scrape_configs:
  - job_name: kubemg
    static_configs:
      - targets: ['kubemg.internal:8443']
    scheme: https
    tls_config:
      # Supply your CA if kubemg uses a self-signed or internal-CA certificate.
      ca_file: /etc/prometheus/kubemg-ca.crt
```

## Next

- [Environment reference](environment.md) — `KUBEMG_METRICS_ENABLED` and all other variables
- [Production checklist](production-checklist.md)
