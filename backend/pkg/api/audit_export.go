package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Taking a filtered result out of the console.
 *
 * Without this, evidence collection for an access review is a screenshot: the
 * trail could be narrowed exactly the right way on screen and there was no way
 * to hand the result to the auditor asking for it, or to keep it beside the
 * ticket it belongs to. An export is not a new disclosure — it answers the
 * question the page already answered, to the same person, under the same
 * narrowing — it is the same answer in a form that can leave.
 *
 * Two rules make it evidence rather than a convenience:
 *
 *   - **It is the query the reader was looking at.** Same filter parsing, same
 *     predicates (`db.auditQuery`), same order. An export that answered a
 *     slightly different question from the screen it came off would be
 *     unreproducible, which is the one property evidence may not have.
 *   - **It never lies about being complete.** Past `db.AuditExportLimit` the file
 *     stops, and it says so in a trailing comment row *and* in a response header
 *     — a truncated CSV that looks whole is worse than no export at all.
 *
 * It is deliberately **not** audited, for the same reason reading the trail is
 * not and reading the recording index is not: this is a read of KubeMG's own
 * records by somebody already entitled to them, and a trail that records every
 * read of itself grows faster than the thing it records. What it cannot do is
 * widen: a non-admin exports their own rows exactly as they read them.
 */

// auditExportColumns is the header row, and the order is not cosmetic: it is
// who, when, from where, against what, and what happened — the order an access
// review is walked through, so the file reads top to bottom the way the question
// is asked.
var auditExportColumns = []string{
	"at",
	"username",
	"user_id",
	"source_addr",
	"user_agent",
	"cluster",
	"namespace",
	"verb",
	"method",
	"path",
	"resource",
	"impersonated_user",
	"impersonated_groups",
	"status",
	"duration_ms",
	"streaming",
	"phase",
	"session_id",
	"guardrail_policy",
	"guardrail_action",
	"error",
}

// exportAudit writes the filtered trail as CSV.
func (s *server) exportAudit(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter, ok := auditFilterFrom(c)
	if !ok {
		return
	}
	// The same narrowing the page applies, applied again rather than trusted:
	// this handler is reachable on its own URL, and a query parameter must not be
	// able to widen what a non-admin may take away any more than it can widen
	// what they may read.
	if !user.IsAdmin() {
		filter.UserID = user.ID
	}
	// Paging is meaningless in a file, and an offset carried over from the page
	// the reader was on would silently drop the rows above it.
	filter.Limit = 0
	filter.Offset = 0

	events, truncated, err := s.store.ExportAuditEvents(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not export the audit trail"})
		return
	}

	filename := fmt.Sprintf("kubemg-audit-%s.csv", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	// Read by the console so it can say the export was cut before the reader
	// files it as the whole story.
	if truncated {
		c.Header("X-Kubemg-Export-Truncated", strconv.Itoa(db.AuditExportLimit))
	}
	c.Status(http.StatusOK)

	writer := csv.NewWriter(c.Writer)
	if err := writer.Write(auditExportColumns); err != nil {
		return
	}
	for _, event := range events {
		if err := writer.Write(auditExportRow(event)); err != nil {
			return
		}
	}
	if truncated {
		// A comment row rather than a data row: the file is evidence and has to
		// carry its own limits, but it must not carry them as something that
		// parses as a record.
		_ = writer.Write([]string{fmt.Sprintf(
			"# truncated at %d rows — narrow the filter and export again",
			db.AuditExportLimit,
		)})
	}
	writer.Flush()
}

// auditExportRow renders one record. Times are RFC3339 in UTC: a spreadsheet's
// idea of a local timestamp is the reader's machine, and this file is going to
// be opened somewhere else.
func auditExportRow(event db.AuditEvent) []string {
	streaming := "false"
	if event.Streaming {
		streaming = "true"
	}
	return []string{
		event.At.UTC().Format(time.RFC3339),
		event.Username,
		strconv.FormatUint(uint64(event.UserID), 10),
		event.SourceAddr,
		event.UserAgent,
		event.Cluster,
		event.Namespace,
		event.Verb,
		event.Method,
		event.Path,
		event.Resource,
		event.ImpersonatedUser,
		strings.Join(event.ImpersonatedGroupList(), " "),
		strconv.Itoa(event.Status),
		strconv.FormatInt(event.DurationMS, 10),
		streaming,
		event.Phase,
		event.SessionID,
		event.GuardrailPolicy,
		event.GuardrailAction,
		event.Error,
	}
}
