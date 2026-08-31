package auditforward

import (
	"context"
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

// encode flattens an event into its JSON body and picks its severity. A failed
// encode yields ok=false rather than a partial record: half an audit line is
// worse on a SIEM than a gap, because it parses.
func encode(ctx context.Context, event bastion.Event) (record, bool) {
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
	if source := bastion.SourceFrom(ctx).Truncate(); source.Addr != "" || source.UserAgent != "" {
		fields["source_addr"] = source.Addr
		fields["user_agent"] = source.UserAgent
	}
	if event.Namespace != "" {
		fields["namespace"] = event.Namespace
	}
	if event.Resource != "" {
		fields["resource"] = event.Resource
	}
	if event.Error != "" {
		fields["error"] = event.Error
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
