# Prometheus metrics

KubeMG can expose a Prometheus scrape endpoint at `GET /metrics` on a **separate internal listener**. It is off by default because the endpoint discloses version, route inventory, and process details — information that must not be reachable from agent clusters on the public port.

## Enabling

Set `KUBEMG_METRICS_ADDR` to a `host:port` the server should bind internally:

```dotenv
KUBEMG_METRICS_ADDR=127.0.0.1:9090
```

KubeMG starts a second listener on that address serving only `/metrics`. Bind it to loopback or a private network interface so it is reachable by your Prometheus scraper but not by anything on the public internet or by agent clusters.

!!! warning "Do not use the public port"
    `KUBEMG_LISTEN_ADDR` is the port `KUBEMG_PUBLIC_URL` points at — the one agent clusters dial into from outside. `KUBEMG_METRICS_ADDR` must be a **different** address. The endpoint discloses the exact build version (CVE-targeting material), per-route traffic and error rates, and process details (goroutine count, heap size, open file descriptors).

## Metrics exposed

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `kubemg_http_requests_total` | Counter | `method`, `path`, `status` | Route pattern, not actual path — `/clusters/:id` rather than `/clusters/42`. Unmatched routes are grouped as `path="unmatched"`. WebSocket connections (tunnel, attach, exec, port-forward) are counted here when the connection closes. |
| `kubemg_http_request_duration_seconds` | Histogram | `method`, `path` | Latency for ordinary HTTP requests. WebSocket upgrades are excluded — they hold the connection for the session lifetime and would make the histogram meaningless. |
| `kubemg_http_requests_in_flight` | Gauge | — | Ordinary HTTP requests currently being served. WebSocket connections excluded for the same reason. |
| `kubemg_db_query_duration_seconds` | Histogram | `operation` | Per GORM operation type: `create`, `query`, `update`, `delete`, `row`, `raw`. |
| `kubemg_db_queries_total` | Counter | `operation`, `error` | `error="true"` means a real database error. `ErrRecordNotFound` is not an error — it is normal flow for existence checks. |
| `kubemg_build_info` | Gauge | `version` | Always `1`. Use the `version` label to correlate anomalies with deploys. |

Go runtime and process metrics (goroutines, GC pauses, file descriptors) come from the standard Prometheus registry and are included automatically.

## Example Prometheus scrape config

```yaml
scrape_configs:
  - job_name: kubemg
    static_configs:
      - targets: ['127.0.0.1:9090']
```

If your Prometheus runs on a different host, bind `KUBEMG_METRICS_ADDR` to the node's private interface instead of loopback, and scope the firewall rule to that scraper.

## Next

- [Environment reference](environment.md) — `KUBEMG_METRICS_ADDR` and all other variables
- [Production checklist](production-checklist.md)
