package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/jit"
)

/*
 * Access approvals in chat.
 *
 * This is the dispatcher's second job and it is a different shape from the first.
 * An alarm is a fact being reported; an approval request is a question waiting for
 * somebody, and the thing that makes it worth delivering at all is that the answer
 * can be given from where it is read. So these two payloads are the only ones here
 * that carry *controls* — Slack Block Kit buttons and a Teams Adaptive Card —
 * rather than fields.
 *
 * Where they go: every enabled channel whose kind is a chat destination. That is a
 * deliberate reading of "notify Slack/Teams" rather than a new selection setting.
 * An administrator who added a Slack channel to KubeMG has already made the egress
 * decision, and who is asking for production access is the same class of
 * information as who was refused it, which those channels already carry. A fleet
 * that wants approvals somewhere else adds a channel for it.
 *
 * Both buttons offer the console first. The Slack primary button and both Teams
 * actions open KubeMG, where the decision is taken under a real session; the
 * signed callback exists for a Slack app with interactivity configured, and it
 * still requires the acting identity to resolve to a KubeMG administrator. A chat
 * client is not an identity provider, and an approval record naming one would be
 * worth less than no record.
 */

// approvalChannelKinds are the destinations an approval request is offered to. An
// Alertmanager or a SIEM is deliberately not one: neither has anybody reading it
// who can answer a question, and filing a pending approval as an alert that never
// resolves is how an on-call rotation learns to ignore a channel.
var approvalChannelKinds = []string{db.ChannelSlack, db.ChannelTeams}

// NotifyAccessRequest delivers a pending elevation request to the chat channels.
//
// It never returns an error and never blocks a caller for long: the workflow it
// belongs to has already committed, and a webhook that has gone slow must not make
// asking for access slow. A channel that rejects the payload is recorded as
// unhealthy exactly like a failed alarm delivery, which is the only place a
// silently-broken integration becomes visible.
func (d *Dispatcher) NotifyAccessRequest(ctx context.Context, note jit.ApprovalNote) {
	if d == nil {
		return
	}
	for _, channel := range d.chatChannels() {
		body, err := d.approvalBody(channel, note)
		if err != nil {
			d.logger.Error("could not render an access approval payload",
				slog.String("channel", channel.Name),
				slog.String("error", err.Error()))
			continue
		}
		d.postNotice(ctx, channel, body, "access request", note.RequestID)
	}
}

// NotifyAccessDecision reports what happened to a request.
//
// It is separate from the request notice rather than a threaded reply because an
// incoming webhook cannot update a message it did not post through a Slack app.
// The alternative — saying nothing — leaves a channel full of questions with no
// answers, which is worse than a second message.
func (d *Dispatcher) NotifyAccessDecision(ctx context.Context, note jit.DecisionNote) {
	if d == nil {
		return
	}
	for _, channel := range d.chatChannels() {
		body, err := d.decisionBody(channel, note)
		if err != nil {
			d.logger.Error("could not render an access decision payload",
				slog.String("channel", channel.Name),
				slog.String("error", err.Error()))
			continue
		}
		d.postNotice(ctx, channel, body, "access decision", note.RequestID)
	}
}

// chatChannels are the enabled chat destinations, read from the cached set the
// dispatcher already refreshes. A dispatcher whose channels have not loaded yet
// has none, which means an approval request that arrives in the first seconds
// after a boot reaches the console only — an acceptable loss against reading the
// database on a path that must not block.
func (d *Dispatcher) chatChannels() []db.AlarmChannel {
	d.mu.RLock()
	defer d.mu.RUnlock()

	out := []db.AlarmChannel{}
	for _, channel := range d.channels {
		if !channel.Enabled {
			continue
		}
		for _, kind := range approvalChannelKinds {
			if channel.Kind == kind {
				out = append(out, channel)
				break
			}
		}
	}
	return out
}

// postNotice delivers one rendered body, recording the verdict on the channel.
// The cool-off does not apply: an approval request is not a repeated fact, and
// suppressing the second one because it looks like the first would be suppressing
// somebody's access.
func (d *Dispatcher) postNotice(
	ctx context.Context, channel db.AlarmChannel, body []byte, kind, id string,
) {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alarmDeliveryTimeout)
	defer cancel()

	if err := d.post(sendCtx, channel, body, "application/json"); err != nil {
		d.logger.Warn("could not deliver an "+kind,
			slog.String("channel", channel.Name),
			slog.String("request", id),
			slog.String("error", err.Error()))
		d.recordDelivery(ctx, channel.ID, DeliveryFailed, err.Error())
		return
	}
	d.recordDelivery(ctx, channel.ID, DeliveryOK, "")
	d.logger.Info("delivered an "+kind,
		slog.String("channel", channel.Name),
		slog.String("kind", channel.Kind),
		slog.String("request", id))
}

func (d *Dispatcher) approvalBody(channel db.AlarmChannel, note jit.ApprovalNote) ([]byte, error) {
	var payload any
	switch channel.Kind {
	case db.ChannelSlack:
		payload = slackApprovalMessage(note)
	case db.ChannelTeams:
		payload = teamsApprovalCard(note)
	default:
		return nil, fmt.Errorf("channel kind %q does not carry approvals", channel.Kind)
	}
	return json.Marshal(payload)
}

func (d *Dispatcher) decisionBody(channel db.AlarmChannel, note jit.DecisionNote) ([]byte, error) {
	var payload any
	switch channel.Kind {
	case db.ChannelSlack:
		payload = slackDecisionMessage(note)
	case db.ChannelTeams:
		payload = teamsDecisionCard(note)
	default:
		return nil, fmt.Errorf("channel kind %q does not carry approvals", channel.Kind)
	}
	return json.Marshal(payload)
}

/* ------------------------------------------------------- slack block kit --- */

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackButton struct {
	Type     string    `json:"type"`
	Text     slackText `json:"text"`
	Style    string    `json:"style,omitempty"`
	URL      string    `json:"url,omitempty"`
	Value    string    `json:"value,omitempty"`
	ActionID string    `json:"action_id,omitempty"`
}

type slackBlock struct {
	Type     string        `json:"type"`
	Text     *slackText    `json:"text,omitempty"`
	Fields   []slackText   `json:"fields,omitempty"`
	Elements []slackButton `json:"elements,omitempty"`
}

type slackBlockMessage struct {
	// Text is the notification line and the fallback for clients that cannot
	// render blocks — a push notification with no text is a silent page.
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// slackApprovalMessage renders the Block Kit request.
//
// Block Kit rather than the attachments the alarm payload uses, because this
// message needs buttons and attachments cannot carry them. The `value` on each
// button is the signed action token, which is what a Slack app's interactivity
// POST delivers to the callback; the `url` on the primary button is the console,
// which is what works with a plain incoming webhook.
func slackApprovalMessage(note jit.ApprovalNote) slackBlockMessage {
	fields := []slackText{
		mrkdwn("*Requester*\n" + orDash(note.Requester)),
		mrkdwn("*Cluster*\n`" + orDash(note.Cluster) + "`"),
		mrkdwn("*Role*\n`" + orDash(note.Role) + "`"),
		mrkdwn("*Window*\n" + humanDuration(note.Duration)),
	}
	if len(note.Namespaces) > 0 {
		fields = append(fields, mrkdwn("*Namespaces*\n`"+strings.Join(note.Namespaces, ", ")+"`"))
	} else {
		// Said explicitly, because "every namespace" is the part of a cluster-admin
		// request an approver most needs to notice.
		fields = append(fields, mrkdwn("*Namespaces*\nall"))
	}

	headline := fmt.Sprintf("%s is requesting %s on %s",
		orDash(note.Requester), orDash(note.Role), orDash(note.Cluster))

	blocks := []slackBlock{
		{Type: "section", Text: ptr(mrkdwn(":lock: *Elevated access request*\n" + headline))},
		{Type: "section", Fields: fields},
		{Type: "section", Text: ptr(mrkdwn("*Reason*\n" + orDash(note.Reason)))},
	}

	buttons := []slackButton{}
	if note.ConsoleURL != "" {
		buttons = append(buttons, slackButton{
			Type:     "button",
			Text:     plain("Review in KubeMG"),
			Style:    "primary",
			URL:      note.ConsoleURL,
			ActionID: "kubemg_jit_review",
		})
	}
	// The signed tokens ride as values. A workspace with no KubeMG app configured
	// simply never posts them back, and the console button above is the whole
	// journey; one with an app gets one-click decisions that are still recorded
	// against the Slack user's KubeMG account.
	if note.ApproveToken != "" {
		buttons = append(buttons,
			slackButton{
				Type:     "button",
				Text:     plain("Approve"),
				Style:    "primary",
				Value:    note.ApproveToken,
				ActionID: "kubemg_jit_approve",
			},
			slackButton{
				Type:     "button",
				Text:     plain("Reject"),
				Style:    "danger",
				Value:    note.RejectToken,
				ActionID: "kubemg_jit_reject",
			})
	}
	if len(buttons) > 0 {
		blocks = append(blocks, slackBlock{Type: "actions", Elements: buttons})
	}
	blocks = append(blocks, slackBlock{
		Type: "context",
		Fields: []slackText{
			mrkdwn("KubeMG · request `" + note.RequestID + "`"),
		},
	})

	return slackBlockMessage{Text: "[ACCESS] " + headline, Blocks: blocks}
}

// slackDecisionMessage reports the outcome.
func slackDecisionMessage(note jit.DecisionNote) slackBlockMessage {
	headline := decisionHeadline(note)
	blocks := []slackBlock{
		{Type: "section", Text: ptr(mrkdwn(decisionEmoji(note.Status) + " *Elevated access " +
			note.Status + "*\n" + headline))},
	}
	if note.Comment != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: ptr(mrkdwn("*Note*\n" + note.Comment)),
		})
	}
	blocks = append(blocks, slackBlock{
		Type:   "context",
		Fields: []slackText{mrkdwn("KubeMG · request `" + note.RequestID + "`")},
	})
	return slackBlockMessage{Text: "[ACCESS] " + headline, Blocks: blocks}
}

func mrkdwn(text string) slackText  { return slackText{Type: "mrkdwn", Text: text} }
func plain(text string) slackText   { return slackText{Type: "plain_text", Text: text} }
func ptr(text slackText) *slackText { return &text }

/* ---------------------------------------------------- teams adaptive card --- */

type teamsCardField struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

type teamsCardElement struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Weight/Size/Wrap are Adaptive Card text properties. They are set explicitly
	// because a card that does not wrap truncates the reason, which is the field
	// the whole approval turns on.
	Weight string           `json:"weight,omitempty"`
	Size   string           `json:"size,omitempty"`
	Wrap   bool             `json:"wrap,omitempty"`
	Facts  []teamsCardField `json:"facts,omitempty"`
}

type teamsCardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

type teamsAdaptiveCard struct {
	Schema  string             `json:"$schema"`
	Type    string             `json:"type"`
	Version string             `json:"version"`
	Body    []teamsCardElement `json:"body"`
	Actions []teamsCardAction  `json:"actions,omitempty"`
}

type teamsAttachment struct {
	ContentType string            `json:"contentType"`
	Content     teamsAdaptiveCard `json:"content"`
}

type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

// teamsApprovalCard renders the Adaptive Card.
//
// Its actions are `Action.OpenUrl` only, and that is not a shortcut: an incoming
// webhook card cannot post back to an arbitrary endpoint — the connector actions
// that could are retired — so a button claiming to approve in place would be a
// button that does nothing. Opening the console is the honest control, and the
// card carries everything needed to decide *whether* to click it.
func teamsApprovalCard(note jit.ApprovalNote) teamsMessage {
	namespaces := "all"
	if len(note.Namespaces) > 0 {
		namespaces = strings.Join(note.Namespaces, ", ")
	}

	body := []teamsCardElement{
		{Type: "TextBlock", Text: "Elevated access request", Weight: "Bolder", Size: "Medium", Wrap: true},
		{Type: "TextBlock", Wrap: true, Text: fmt.Sprintf("%s is requesting %s on %s.",
			orDash(note.Requester), orDash(note.Role), orDash(note.Cluster))},
		{Type: "FactSet", Facts: []teamsCardField{
			{Title: "Requester", Value: orDash(note.Requester)},
			{Title: "Cluster", Value: orDash(note.Cluster)},
			{Title: "Role", Value: orDash(note.Role)},
			{Title: "Namespaces", Value: namespaces},
			{Title: "Window", Value: humanDuration(note.Duration)},
			{Title: "Request", Value: note.RequestID},
		}},
		{Type: "TextBlock", Weight: "Bolder", Text: "Reason", Wrap: true},
		{Type: "TextBlock", Wrap: true, Text: orDash(note.Reason)},
	}

	actions := []teamsCardAction{}
	if note.ConsoleURL != "" {
		actions = append(actions,
			teamsCardAction{Type: "Action.OpenUrl", Title: "Review in KubeMG", URL: note.ConsoleURL})
	}

	return teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content: teamsAdaptiveCard{
				Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
				Type:    "AdaptiveCard",
				Version: "1.4",
				Body:    body,
				Actions: actions,
			},
		}},
	}
}

func teamsDecisionCard(note jit.DecisionNote) teamsMessage {
	facts := []teamsCardField{
		{Title: "Requester", Value: orDash(note.Requester)},
		{Title: "Cluster", Value: orDash(note.Cluster)},
		{Title: "Role", Value: orDash(note.Role)},
		{Title: "Decision", Value: note.Status},
		{Title: "By", Value: orDash(note.Decider)},
		{Title: "Request", Value: note.RequestID},
	}
	if note.ExpiresAt != nil {
		facts = append(facts, teamsCardField{
			Title: "Expires",
			Value: note.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}

	body := []teamsCardElement{
		{Type: "TextBlock", Text: "Elevated access " + note.Status, Weight: "Bolder", Size: "Medium", Wrap: true},
		{Type: "TextBlock", Wrap: true, Text: decisionHeadline(note)},
		{Type: "FactSet", Facts: facts},
	}
	if note.Comment != "" {
		body = append(body, teamsCardElement{Type: "TextBlock", Wrap: true, Text: note.Comment})
	}

	return teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content: teamsAdaptiveCard{
				Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
				Type:    "AdaptiveCard",
				Version: "1.4",
				Body:    body,
			},
		}},
	}
}

/* ----------------------------------------------------------------- shared --- */

func decisionHeadline(note jit.DecisionNote) string {
	switch note.Status {
	case db.JitStatusActive, db.JitStatusApproved:
		line := fmt.Sprintf("%s granted %s %s on %s",
			orDash(note.Decider), orDash(note.Requester), orDash(note.Role), orDash(note.Cluster))
		if note.ExpiresAt != nil {
			line += ", until " + note.ExpiresAt.UTC().Format("15:04 MST on 2 Jan")
		}
		return line
	case db.JitStatusRejected:
		return fmt.Sprintf("%s rejected %s's request for %s on %s",
			orDash(note.Decider), orDash(note.Requester), orDash(note.Role), orDash(note.Cluster))
	default:
		return fmt.Sprintf("%s's %s on %s has ended (%s)",
			orDash(note.Requester), orDash(note.Role), orDash(note.Cluster), note.Status)
	}
}

func decisionEmoji(status string) string {
	switch status {
	case db.JitStatusActive, db.JitStatusApproved:
		return ":white_check_mark:"
	case db.JitStatusRejected:
		return ":no_entry:"
	default:
		return ":hourglass_flowing_sand:"
	}
}

// humanDuration renders a window the way an approver reads one: "4 hours", not
// "4h0m0s".
func humanDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := minutes / 60
	rest := minutes % 60
	unit := "hours"
	if hours == 1 {
		unit = "hour"
	}
	if rest == 0 {
		return fmt.Sprintf("%d %s", hours, unit)
	}
	return fmt.Sprintf("%d %s %d minutes", hours, unit, rest)
}
