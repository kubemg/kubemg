package auditforward

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
)

/*
 * What a forwarded record looks like on the wire.
 *
 * The field names are the SlogAuditor's, exactly — `timestamp`, `username`,
 * `uri`, `impersonate_user`, `status_code` and the rest. That is the point: a
 * site already tailing this server's stdout has a parser for that shape, and a
 * second shape here would mean maintaining two parsers for one trail and
 * discovering the drift only when a detection rule quietly stops matching.
 *
 * The manifest diff is not carried, for the same reason the structured log does
 * not carry it: it is the one audit field that can hold a Secret's worth of
 * content, it is bounded by nothing useful, and syslog is the transport least
 * able to survive a large record. It stays in the table, which is where the
 * setting that produces it says it goes.
 */

// severity is the syslog severity a record is sent at. It mirrors the level
// SlogAuditor logs at, so "filter the stream down to refusals" is the same
// query against either copy.
const (
	severityWarning = 4
	severityInfo    = 6
)

// record is one audit event, flattened for the wire.
type record struct {
	body     []byte
	severity int
	at       time.Time
}

// pending is one event waiting to be encoded in the sender goroutine. The
// source info is extracted on the caller's goroutine while ctx is live; the
// map allocation and json.Marshal happen in the drain where they do not add
// to the proxied request's latency.
type pending struct {
	event      bastion.Event
	sourceAddr string
	userAgent  string
}

// encode flattens an event into its JSON body and picks its severity. A failed
// encode yields ok=false rather than a partial record: half an audit line is
// worse on a SIEM than a gap, because it parses.
func encode(p pending) (record, bool) {
	event := p.event
	fields := map[string]any{
		"audit":              "kubemg.proxy",
		"timestamp":          event.At.UTC().Format(time.RFC3339Nano),
		"user_id":            event.UserID,
		"username":           event.Username,
		"cluster_id":         event.ClusterID,
		"cluster":            event.Cluster,
		"verb":               event.Verb,
		"method":             event.Method,
		"uri":                event.Path,
		"impersonate_user":   event.ImpersonatedUser,
		"impersonate_groups": strings.Join(event.ImpersonatedGroups, ","),
		"status_code":        event.Status,
		"duration_ms":        event.Duration.Milliseconds(),
	}
	if p.sourceAddr != "" || p.userAgent != "" {
		fields["source_addr"] = p.sourceAddr
		fields["user_agent"] = p.userAgent
	}
	if event.Namespace != "" {
		fields["namespace"] = event.Namespace
	}
	if event.Resource != "" {
		fields["resource"] = event.Resource
	}
	if event.Name != "" {
		fields["name"] = event.Name
	}
	if event.Error != "" {
		fields["error"] = event.Error
	}
	if event.RefusedBy != "" {
		fields["refused_by"] = event.RefusedBy
	}
	if event.Streaming {
		fields["streaming"] = true
		fields["phase"] = event.Phase
	}
	if event.SessionID != "" {
		fields["session_id"] = event.SessionID
	}
	if event.GuardrailPolicy != "" {
		fields["guardrail_policy"] = event.GuardrailPolicy
		fields["guardrail_action"] = event.GuardrailAction
	}
	if event.BytesOut != 0 || event.BytesIn != 0 {
		fields["bytes_out"] = event.BytesOut
		fields["bytes_in"] = event.BytesIn
	}

	body, err := json.Marshal(fields)
	if err != nil {
		return record{}, false
	}

	severity := severityInfo
	if event.Error != "" || event.Status >= http.StatusBadRequest {
		severity = severityWarning
	}

	at := event.At
	if at.IsZero() {
		at = time.Now()
	}
	return record{body: body, severity: severity, at: at.UTC()}, true
}
