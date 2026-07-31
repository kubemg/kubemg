package bastion

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/guardrails"
)

/*
 * Guardrails at the gateway.
 *
 * This is the one refusal KubeMG makes on its own authority. Every other check
 * here either resolves who the caller is or narrows what their grant covers, and
 * the substantive "may they" is answered by the target cluster's RBAC through
 * impersonation. A guardrail is different by design: it stops calls the caller is
 * fully entitled to make, because the cost of the mistake is out of proportion to
 * the ease of making it.
 *
 * It goes in two places, because a destructive act arrives in two shapes:
 *
 *   - the API call, checked in Handle and Call before anything reaches a tunnel;
 *   - the command, checked twice — the argv of a non-interactive exec at stream
 *     open, and each line typed into an interactive shell as it is entered.
 *
 * The second half is what makes this more than an admission webhook. `kubectl
 * exec -it pod -- bash` is one API call, and everything destructive that happens
 * inside it happens afterwards, inside a session the API server sees as a single
 * connection. Nothing cluster-side can see those keystrokes. The bastion can,
 * because it is already carrying them.
 */

// guardrailBlocked is the flag on a refusal body. A client that meets a 403 has
// no other way to tell a KubeMG policy apart from the cluster's own RBAC saying
// no, and the two call for completely different next steps.
const guardrailBlocked = "guardrail_blocked"

// guardAPIRequest evaluates a proxied call. It returns the decision so the
// caller can record it; a nil decision, or one that only warns, allows the call.
func (p *Proxy) guardAPIRequest(clusterID uint, method, path string) *guardrails.Decision {
	return p.guard.EvaluateAPIRequest(clusterID, method, path)
}

// guardCommand evaluates a command about to run inside a container.
func (p *Proxy) guardCommand(clusterID uint, command string) *guardrails.Decision {
	return p.guard.EvaluateTerminalInput(clusterID, command)
}

// noteGuardrail annotates an audit event with the rule that matched. It is
// written for the warn case as well as the block case — a rule running in warn
// exists precisely to produce these rows, and a warn that left no trace would
// make the mode pointless.
func noteGuardrail(event *Event, decision *guardrails.Decision) {
	if decision == nil {
		return
	}
	event.GuardrailPolicy = decision.Policy
	event.GuardrailAction = decision.Action
	if decision.Blocked() {
		event.Error = decision.Message()
	}
}

// failGuardrail refuses a call and records it.
//
// It is a separate path from fail because the body carries one extra field: a
// client, and the console in particular, has to be able to tell "KubeMG refused
// this" from "the cluster refused this" without parsing prose.
func (p *Proxy) failGuardrail(c *gin.Context, event *Event, decision *guardrails.Decision) {
	message := decision.Message()

	event.Status = http.StatusForbidden
	event.Error = message
	noteGuardrail(event, decision)
	event.Duration = time.Since(event.At)
	p.auditor.Record(c.Request.Context(), *event)

	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error":          message,
		guardrailBlocked: true,
		"policy":         decision.Policy,
		"scope":          decision.Scope,
	})
}

// recordGuardedCommand writes a trail record for one command matched inside a
// live session.
//
// It is its own record rather than an annotation on the session's, and it has to
// be: a session is recorded twice, at open and at close, and both describe the
// shell rather than anything typed into it. Without this, a command KubeMG
// refused at 03:00 would appear nowhere — the cluster never saw it, and the
// session's own two rows look exactly like a shell where nothing happened.
//
// The verb is the session's, the path is the session's, and the guardrail fields
// carry which rule fired. What was typed is deliberately *not* recorded here: the
// trail is queried far more widely than a recording is, and a refused command
// line is exactly the kind of thing that holds a mistyped password. The session
// recording already has it, behind the capability that governs watching one.
func (p *Proxy) recordGuardedCommand(c *gin.Context, event *Event, decision *guardrails.Decision) {
	record := *event
	record.At = time.Now().UTC()
	record.Phase = ""
	record.Duration = 0
	record.BytesOut = 0
	record.BytesIn = 0
	noteGuardrail(&record, decision)
	if decision.Blocked() {
		record.Status = http.StatusForbidden
	}
	p.auditor.Record(c.Request.Context(), record)
}

// execCommand renders the argv of a non-interactive exec for matching.
//
// This is the half the keystroke guard cannot see. `kubectl exec pod -- rm -rf /`
// never types anything: the command is in the query string of the upgrade
// request, runs immediately, and the only bytes on the stream are its output. A
// guardrail that watched keystrokes alone would be trivially avoided by putting
// the command on the kubectl command line, which is also the form a script uses.
func execCommand(path string) string {
	command := queryOf(path)["command"]
	if len(command) == 0 {
		return ""
	}
	return strings.Join(command, " ")
}
