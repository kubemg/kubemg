package observability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/jit"
)

// The reason is the requester's own words, and an approver reads it beside the
// controls that grant access. In Slack it is plain text and every value in
// mrkdwn is escaped; in Teams it is a TextRun, which reads no Markdown. A
// mention, a relabelled link or lines dressed as the card's own fields must
// arrive as the characters that were typed.
func TestARequesterCannotWriteMarkupIntoTheApprovalMessage(t *testing.T) {
	reason := "<!channel> please <https://evil.example/login|Review in KubeMG>\n*Role*\n`view` [Approve](https://evil.example)"
	note := jit.ApprovalNote{
		RequestID: "r1", Requester: "dev<@U123>", Cluster: "prod", Role: "cluster-admin",
		Namespaces: []string{"shop"}, Duration: time.Hour, Reason: reason,
		ConsoleURL: "https://kubemg.example.com/jit/r1",
	}

	slack := slackApprovalMessage(note)
	var sawReason bool
	for _, block := range slack.Blocks {
		texts := block.Fields
		if block.Text != nil {
			texts = append(texts, *block.Text)
		}
		for _, text := range texts {
			if text.Type == "plain_text" && text.Text == reason {
				sawReason = true
				continue
			}
			if text.Type == "mrkdwn" && (strings.Contains(text.Text, "<!channel>") ||
				strings.Contains(text.Text, "<https://evil") || strings.Contains(text.Text, "<@U123>")) {
				t.Fatalf("live markup in a mrkdwn block: %q", text.Text)
			}
		}
	}
	if !sawReason {
		t.Fatal("the reason is not carried as plain text")
	}
	if strings.Contains(slack.Text, "<@U123>") {
		t.Fatalf("live markup in the notification text: %q", slack.Text)
	}

	card, err := json.Marshal(teamsApprovalCard(note))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Attachments []struct {
			Content struct {
				Body []teamsCardElement `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(card, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, element := range parsed.Attachments[0].Content.Body {
		if element.Type == "TextBlock" && strings.Contains(element.Text, "evil.example") {
			t.Fatalf("the reason reached a Markdown TextBlock: %q", element.Text)
		}
		if element.Type == "RichTextBlock" && len(element.Inlines) == 1 && element.Inlines[0].Text == reason {
			return
		}
	}
	t.Fatal("the reason is not carried as a TextRun")
}

func TestADecisionNoteIsPlainText(t *testing.T) {
	note := jit.DecisionNote{RequestID: "r1", Requester: "dev", Cluster: "prod", Role: "view",
		Status: "rejected", Decider: "root", Comment: "<!here> see <https://evil.example|this>"}
	for _, block := range slackDecisionMessage(note).Blocks {
		if block.Text != nil && block.Text.Type == "mrkdwn" && strings.Contains(block.Text.Text, "<!here>") {
			t.Fatalf("the comment reached mrkdwn: %q", block.Text.Text)
		}
	}
}
