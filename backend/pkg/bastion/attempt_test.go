package bastion

import (
	"context"
	"testing"
)

// An unrelated refusal earlier in the same request must not hide KubeMG's own:
// discovery answering 404 for an absent group says nothing about the 403 the
// pre-flight gives after it. Only the status already recorded is "in the trail".
func TestAnEarlierRefusalDoesNotHideADifferentOne(t *testing.T) {
	ctx, attempt := WithAttempt(context.Background())
	auditor := NoteAttempts(&recordingAuditor{})

	auditor.Record(ctx, Event{Status: 200})
	if attempt.RefusalRecorded(403) {
		t.Fatal("a success marked a refusal as recorded")
	}

	auditor.Record(ctx, Event{Status: 404})
	if !attempt.RefusalRecorded(404) {
		t.Fatal("the recorded 404 is not noted")
	}
	if attempt.RefusalRecorded(403) {
		t.Fatal("a recorded 404 hides a 403 nobody wrote down")
	}

	auditor.Record(ctx, Event{Status: 502, Error: "tunnel closed"})
	if attempt.RefusalRecorded(403) {
		t.Fatal("a recorded tunnel failure hides a 403 nobody wrote down")
	}

	var none *Attempt
	if none.RefusalRecorded(403) {
		t.Fatal("no attempt reads as a recorded refusal")
	}
}
