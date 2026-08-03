package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

/*
 * The console's time range.
 *
 * "The last hour" has to mean the same span in a chart, in the audit trail and
 * in a link somebody pastes into a ticket. None of those agree if the browser
 * computes the boundary — three surfaces each subtracting from their own clock
 * produce three windows, and a row count that disagrees with the page it counts
 * is a trail nobody trusts. So the vocabulary is a fixed table here and the
 * boundary is resolved server-side, from this process's clock, for every surface
 * that takes a range.
 *
 * It is a table rather than a parsed duration for the same reason the audit
 * filter has always used one: the set the console offers and the set the API
 * accepts cannot drift apart, and no caller can ask for a window wide enough to
 * be a table scan dressed as a filter.
 */

// consoleRanges are the presets every ranged surface shares.
var consoleRanges = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// rangeSpan resolves the `range` parameter into a span back from now.
//
// `all` is the one preset whose meaning is a property of the surface rather than
// of the vocabulary, so the caller supplies it: for the audit trail it is "no
// lower bound at all", and for a query against a datasource it is the widest
// window that path allows — a metrics backend has retention, and asking for
// everything it holds is asking for as far back as the query is permitted to
// reach. Zero means "no preset", which is also what an absent parameter gives.
func rangeSpan(c *gin.Context, all time.Duration) (time.Duration, bool) {
	raw := strings.ToLower(strings.TrimSpace(c.Query("range")))
	if raw == "" {
		return 0, true
	}
	if raw == "all" {
		return all, true
	}
	window, known := consoleRanges[raw]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "range must be one of 15m, 1h, 6h, 24h, 7d, 30d or all",
		})
		return 0, false
	}
	return window, true
}
