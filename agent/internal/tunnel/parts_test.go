package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubemg/kubemg/agent/internal/kube"
	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// queuedWriter is a writer with no socket and nothing draining it, so a test
// can read what was queued, and in which queue.
func queuedWriter(bulk int) *writer {
	return &writer{
		urgent: make(chan []byte, outboundQueue),
		bulk:   make(chan []byte, bulk),
		logger: slog.Default(),
		done:   make(chan struct{}),
	}
}

// apiServing starts a stand-in API server answering every request with body,
// and an agent client pointed at it.
func apiServing(t *testing.T, body []byte) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("sa-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	kubeClient, err := kube.New(kube.Options{APIURL: server.URL, InsecureSkipVerify: true, TokenPath: tokenPath})
	if err != nil {
		t.Fatalf("build kube client: %v", err)
	}
	client, err := New(Options{BastionURL: "https://kubemg.example.com", Token: "kmg_test", Kube: kubeClient})
	if err != nil {
		t.Fatalf("build tunnel client: %v", err)
	}
	return client
}

// relay runs forwardInParts for a body of the given size and returns the parts
// that were queued and the final response.
func relay(t *testing.T, size int) ([][]byte, protocol.Response) {
	t.Helper()

	body := bytes.Repeat([]byte("k"), size)
	client := apiServing(t, body)
	out := queuedWriter(bulkQueue)

	var parts [][]byte
	drained := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case payload := <-out.bulk:
				var msg protocol.Message
				if err := json.Unmarshal(payload, &msg); err != nil || msg.Type != protocol.MessageResponsePart {
					t.Errorf("a non-part frame was queued as a part: %s", payload)
					continue
				}
				parts = append(parts, msg.ResponsePart.Data)
			case <-stop:
				return
			}
		}
	}()

	final := client.forwardInParts(context.Background(), "1-1", protocol.Request{Method: http.MethodGet, Path: "/api/v1/pods"}, out)
	// Everything forwardInParts queued is already in the channel; let the
	// drainer take it before stopping it.
	for len(out.bulk) > 0 {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-drained
	return parts, final
}

// The point of the change on this side: a body larger than one part goes as
// several, none larger than ResponsePartSize, and the parts plus the final
// frame's body are the body, in order.
func TestALargeBodyIsRelayedInParts(t *testing.T) {
	size := protocol.ResponsePartSize*2 + 1234
	parts, final := relay(t, size)

	if final.Error != "" {
		t.Fatalf("relay failed: %s", final.Error)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	total := len(final.Body)
	for _, part := range parts {
		if len(part) > protocol.ResponsePartSize {
			t.Fatalf("a part of %d bytes is larger than ResponsePartSize", len(part))
		}
		total += len(part)
	}
	if total != size || len(final.Body) != 1234 {
		t.Fatalf("relayed %d bytes with a %d-byte tail, want %d with 1234", total, len(final.Body), size)
	}
	if final.Status != http.StatusOK || http.Header(final.Header).Get("Content-Type") != "application/json" {
		t.Fatalf("the final frame lost the response head: %d %v", final.Status, final.Header)
	}
}

// A body that is an exact multiple of the part size ends on an empty final
// frame rather than a missing one: the final frame is what carries the status.
func TestABodyOfWholePartsEndsOnAnEmptyFinalFrame(t *testing.T) {
	parts, final := relay(t, protocol.ResponsePartSize*2)

	if final.Error != "" || len(final.Body) != 0 || len(parts) != 2 {
		t.Fatalf("parts=%d tail=%d error=%q, want 2 parts and an empty final frame", len(parts), len(final.Body), final.Error)
	}
}

// A small body — most of them — needs no parts at all.
func TestASmallBodyNeedsNoParts(t *testing.T) {
	parts, final := relay(t, 512)

	if len(parts) != 0 || len(final.Body) != 512 {
		t.Fatalf("parts=%d tail=%d, want the whole body on the final frame", len(parts), len(final.Body))
	}
}

// The size cap is the one a whole response has. Past it the final frame
// carries the error, which is what tells the bastion to drop the parts it has.
func TestABodyPastTheCapEndsInAnError(t *testing.T) {
	_, final := relay(t, kube.MaxResponseBody+1)

	if final.Error != kube.ErrResponseTooLarge.Error() {
		t.Fatalf("final error = %q, want the size cap's", final.Error)
	}
}

// A response is bulk and goes in the bulk queue; a bastion that does not take
// parts still gets it whole.
func TestAWholeResponseIsQueuedAsBulk(t *testing.T) {
	client := apiServing(t, []byte(`{"kind":"PodList"}`))
	out := queuedWriter(bulkQueue)

	client.serveRequest(context.Background(), "1-1", protocol.Request{Method: http.MethodGet, Path: "/api/v1/pods"}, out, false)

	if len(out.urgent) != 0 || len(out.bulk) != 1 {
		t.Fatalf("urgent=%d bulk=%d, want the response in the bulk queue", len(out.urgent), len(out.bulk))
	}
	var msg protocol.Message
	if err := json.Unmarshal(<-out.bulk, &msg); err != nil || msg.Response == nil {
		t.Fatalf("the queued frame is not a response: %v", err)
	}
	if string(msg.Response.Body) != `{"kind":"PodList"}` {
		t.Fatalf("whole response body = %q", msg.Response.Body)
	}
}

// Terminal output queued behind a list goes out first.
func TestUrgentFramesOvertakeQueuedBulk(t *testing.T) {
	out := queuedWriter(bulkQueue)
	out.bulk <- []byte("list part 1")
	out.bulk <- []byte("list part 2")
	out.urgent <- []byte("terminal output")

	if got, _ := out.next(); string(got) != "terminal output" {
		t.Fatalf("first frame out = %q, want the terminal output", got)
	}
	if got, _ := out.next(); string(got) != "list part 1" {
		t.Fatalf("second frame out = %q, want the bulk frames in order", got)
	}
}

// A stream that never stops cannot starve a response into its timeout.
func TestBulkGetsATurnUnderSustainedUrgentTraffic(t *testing.T) {
	out := queuedWriter(bulkQueue)
	out.bulk <- []byte("bulk")
	for range urgentBurst * 2 {
		out.urgent <- []byte("urgent")
	}

	for i := 0; i < urgentBurst; i++ {
		if got, _ := out.next(); string(got) != "urgent" {
			t.Fatalf("frame %d = %q, want urgent frames first", i, got)
		}
	}
	if got, _ := out.next(); string(got) != "bulk" {
		t.Fatalf("after %d urgent frames got %q, want the waiting bulk frame", urgentBurst, got)
	}
}

// A full bulk queue makes a response wait on its own deadline, and give up when
// that is spent — it never blocks past it.
func TestAFullBulkQueueWaitsOnTheCallersDeadline(t *testing.T) {
	out := queuedWriter(1)
	out.bulk <- []byte("occupied")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := out.sendBulk(ctx, protocol.Message{Type: protocol.MessageResponse, ID: "1-1", Response: &protocol.Response{}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sendBulk on a full queue = %v, want the deadline", err)
	}
}

// Past the concurrency ceiling a call waits for a slot, and one whose deadline
// passes first is told so rather than run.
func TestACallPastTheCeilingWaitsForASlot(t *testing.T) {
	client := apiServing(t, nil)
	for range maxConcurrentRequests {
		if !client.acquire(context.Background()) {
			t.Fatal("a free slot was refused")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if client.acquire(ctx) {
		t.Fatal("a call past the ceiling got a slot")
	}

	client.release()
	if !client.acquire(context.Background()) {
		t.Fatal("a released slot was not handed on")
	}
}
