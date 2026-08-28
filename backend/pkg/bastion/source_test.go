package bastion

import (
	"context"
	"strings"
	"testing"
)

/*
 * The source travels on the context so that no event site has to remember it.
 * What that buys has to be asserted where it is cashed in: the row the auditor
 * writes carries the caller, and a record with no caller carries nothing rather
 * than something invented.
 */

func TestRecordedRowCarriesTheCaller(t *testing.T) {
	auditor := NewStoreAuditor(&queueSink{}, nil, nil)

	ctx := WithSource(context.Background(), RequestSource{
		Addr:      "10.4.2.9",
		UserAgent: "kubectl/v1.31.0 (linux/amd64)",
	})
	auditor.Record(ctx, Event{Verb: "delete", Status: 200})

	rows := drain(auditor)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if rows[0].SourceAddr != "10.4.2.9" {
		t.Fatalf("expected the caller's address, got %q", rows[0].SourceAddr)
	}
	if rows[0].UserAgent != "kubectl/v1.31.0 (linux/amd64)" {
		t.Fatalf("expected the caller's user agent, got %q", rows[0].UserAgent)
	}
}

func TestARecordWithNoCallerCarriesNoSource(t *testing.T) {
	// The JIT expirer and the alarm poller are things this server did on its
	// own. An invented address would be worse than an empty column, because an
	// empty one is readable as "not recorded" and a wrong one is not readable as
	// anything.
	auditor := NewStoreAuditor(&queueSink{}, nil, nil)
	auditor.Record(context.Background(), Event{Verb: "jit-expire", Status: 200})

	rows := drain(auditor)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if rows[0].SourceAddr != "" || rows[0].UserAgent != "" {
		t.Fatalf("expected no source at all, got %+v", rows[0])
	}
}

func TestAnEmptySourceIsNotStoredOnTheContext(t *testing.T) {
	// Otherwise a lookup comes back present-but-blank, which reads as "we looked
	// and there was nothing there" rather than "nobody ever looked".
	ctx := WithSource(context.Background(), RequestSource{})
	if got := SourceFrom(ctx); got.Addr != "" || got.UserAgent != "" {
		t.Fatalf("expected the zero source, got %+v", got)
	}
	if SourceFrom(nil).Addr != "" {
		t.Fatal("a nil context has no source and must not panic")
	}
}

func TestAUserAgentIsBoundedOnTheWayIn(t *testing.T) {
	// It is a header a client writes, so it is as long as the client decided.
	source := RequestSource{
		Addr:      "10.4.2.9",
		UserAgent: strings.Repeat("a", maxUserAgent*3),
	}.Truncate()

	if len(source.UserAgent) != maxUserAgent {
		t.Fatalf("expected the user agent bounded to %d, got %d", maxUserAgent, len(source.UserAgent))
	}
	if source.Addr != "10.4.2.9" {
		t.Fatalf("a short address must survive untouched, got %q", source.Addr)
	}
}
