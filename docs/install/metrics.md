# Prometheus metrics

kubemg can expose a Prometheus scrape endpoint at `GET /metrics` on a **separate internal listener**. This page says how to turn it on and what it reports. It is off by default because it discloses version, route inventory and process details that agent clusters on the public port must not see.

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
| `kubemg_audit_records_dropped_total` | Counter | `sink` | Audit records discarded because a sink's queue was full. `sink="store"` is the database table the audit page reads; `sink="forward"` is the push to your syslog collector. Both series start at `0`. |
| `kubemg_audit_queue_depth` | Gauge | `sink` | Records waiting in that sink's queue. Each queue holds 4096; a record that arrives while it is full is dropped. |

Go runtime and process metrics (goroutines, GC pauses, file descriptors) come from the standard Prometheus registry and are included automatically.

## Dropped audit records

Neither audit sink ever makes a `kubectl` wait. When the database or collector
falls behind, its queue fills and further records are dropped. A non-zero
`kubemg_audit_records_dropped_total` means the audit page (`store`) or your SIEM
(`forward`) is missing records for that period. The server's own log stream
still carries every one, so recover the gap from there. Alert on any increase:

```yaml
- alert: KubemgAuditRecordsDropped
  expr: increase(kubemg_audit_records_dropped_total[10m]) > 0
```

A queue depth that climbs toward 4096 and stays there is the warning before
the drops start.

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
