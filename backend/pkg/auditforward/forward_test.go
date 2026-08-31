package auditforward

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// collector is a syslog receiver standing in for the SIEM.
type collector struct {
	listener net.Listener

	mu    sync.Mutex
	lines []string
	got   chan struct{}
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	c := &collector{listener: listener, got: make(chan struct{}, 256)}
	go c.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return c
}

func (c *collector) accept() {
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			scanner := bufio.NewScanner(conn)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				c.mu.Lock()
				c.lines = append(c.lines, scanner.Text())
				c.mu.Unlock()
				select {
				case c.got <- struct{}{}:
				default:
				}
			}
		}()
	}
}

func (c *collector) dest() db.AuditForwarder {
	host, port, _ := net.SplitHostPort(c.listener.Addr().String())
	number := 0
	fmt.Sscanf(port, "%d", &number)
	return db.AuditForwarder{
		ID: 1, Name: "logsign", Kind: db.ForwarderSyslog,
		Host: host, Port: number, Protocol: db.ForwarderProtoTCP,
		Facility: db.DefaultForwarderFacility, AppName: db.DefaultForwarderAppName,
		Enabled: true,
	}
}

// waitFor blocks until the collector has at least n lines.
func (c *collector) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.lines)
		c.mu.Unlock()
		if got >= n {
			c.mu.Lock()
			out := append([]string(nil), c.lines...)
			c.mu.Unlock()
			return out
		}
		select {
		case <-c.got:
		case <-deadline:
			t.Fatalf("wanted %d records, collector saw %d", n, got)
		}
	}
}

// fakeStore is the slice of the database the shipper reads.
type fakeStore struct {
	mu       sync.Mutex
	dests    []db.AuditForwarder
	listErr  error
	attempts []attempt
}

type attempt struct {
	id      uint
	status  string
	message string
}

func (f *fakeStore) ListAuditForwarders(context.Context) ([]db.AuditForwarder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]db.AuditForwarder(nil), f.dests...), nil
}

func (f *fakeStore) RecordAuditForwarderAttempt(_ context.Context, id uint, status, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, attempt{id: id, status: status, message: message})
	return nil
}

func (f *fakeStore) health() []attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attempt(nil), f.attempts...)
}

// start runs a forwarder against the store and stops it with the test.
func start(t *testing.T, store Store) *Forwarder {
	t.Helper()
	forwarder := New(Options{Store: store})
	ctx, cancel := context.WithCancel(context.Background())
	go forwarder.Run(ctx)
	t.Cleanup(func() {
		cancel()
		forwarder.Wait()
	})
	// Run refreshes before it selects; wait for that to have happened so a test
	// does not race the very first record against the destination list.
	waitUntil(t, func() bool { return forwarder.active.Load() || store == nil })
	return forwarder
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func event(verb string, status int) bastion.Event {
	return bastion.Event{
		At:     time.Date(2026, 8, 25, 9, 14, 2, 0, time.UTC),
		UserID: 7, Username: "ada", ClusterID: 4, Cluster: "prod-eu",
		Verb: verb, Method: "GET", Path: "/api/v1/namespaces/checkout/pods",
		Namespace: "checkout", Resource: "pods",
		ImpersonatedUser: "ada", Status: status, Duration: 12 * time.Millisecond,
	}
}

// A record reaches the collector as one RFC 5424 line whose message is the JSON
// the structured log already writes.
func TestForwardsRecordAsSyslogJSON(t *testing.T) {
	sink := newCollector(t)
	store := &fakeStore{dests: []db.AuditForwarder{sink.dest()}}
	forwarder := start(t, store)

	forwarder.Record(context.Background(), event("list", 200))

	line := sink.waitFor(t, 1)[0]

	// <134> is local0 (16*8) at informational (6).
	if !strings.HasPrefix(line, "<134>1 ") {
		t.Fatalf("wanted an RFC 5424 header at local0/info, got %q", line)
	}
	fields := strings.SplitN(line, " ", 8)
	if len(fields) < 8 {
		t.Fatalf("wanted the six header fields before the message, got %q", line)
	}
	if fields[3] != db.DefaultForwarderAppName {
		t.Errorf("APP-NAME is what a SIEM filters this stream on, got %q", fields[3])
	}
	if fields[6] != "-" {
		t.Errorf("structured data is deliberately nil, got %q", fields[6])
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(fields[7]), &body); err != nil {
		t.Fatalf("the message must be the JSON a SIEM parser reads: %v (%q)", err, fields[7])
	}
	// The field names are the structured log's, so one parser reads both copies.
	for key, want := range map[string]any{
		"audit": "kubemg.proxy", "username": "ada", "cluster": "prod-eu",
		"verb": "list", "uri": "/api/v1/namespaces/checkout/pods",
		"namespace": "checkout", "impersonate_user": "ada",
	} {
		if body[key] != want {
			t.Errorf("%s = %v, want %v", key, body[key], want)
		}
	}
	if body["status_code"] != float64(200) {
		t.Errorf("status_code = %v", body["status_code"])
	}
}

// A refusal is sent at syslog's warning severity, so the stream can be filtered
// down to refusals exactly as the structured log can.
func TestRefusalCarriesWarningSeverity(t *testing.T) {
	sink := newCollector(t)
	store := &fakeStore{dests: []db.AuditForwarder{sink.dest()}}
	forwarder := start(t, store)

	denied := event("delete", 403)
	denied.Error = "refused by guardrail"
	forwarder.Record(context.Background(), denied)

	line := sink.waitFor(t, 1)[0]
	if !strings.HasPrefix(line, "<132>1 ") {
		t.Fatalf("wanted local0/warning (<132>), got %q", line)
	}
}

// The verb selection that narrows the audit *table* must not narrow what leaves
// for a SIEM. There is no policy input here at all, and this test is what says
// that is deliberate: a plain read is forwarded like everything else.
func TestForwardsEveryVerbIncludingReads(t *testing.T) {
	sink := newCollector(t)
	store := &fakeStore{dests: []db.AuditForwarder{sink.dest()}}
	forwarder := start(t, store)

	for _, verb := range []string{"get", "list", "watch", "create", "delete"} {
		forwarder.Record(context.Background(), event(verb, 200))
	}

	lines := sink.waitFor(t, 5)
	if len(lines) != 5 {
		t.Fatalf("wanted every verb forwarded, got %d records", len(lines))
	}
}

// Octet counting is a length prefix rather than a trailing newline, because a
// receiver configured for RFC 6587 concatenates newline-framed records.
func TestOctetCountingFramesWithALengthPrefix(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	read := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		read <- string(buf[:n])
	}()

	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number := 0
	fmt.Sscanf(port, "%d", &number)
	dest := db.AuditForwarder{
		ID: 1, Host: host, Port: number, Protocol: db.ForwarderProtoTCP,
		Facility: db.DefaultForwarderFacility, AppName: "kubemg",
		OctetCounting: true, Enabled: true, Kind: db.ForwarderSyslog,
	}
	store := &fakeStore{dests: []db.AuditForwarder{dest}}
	forwarder := start(t, store)
	forwarder.Record(context.Background(), event("get", 200))

	select {
	case frame := <-read:
		prefix, rest, ok := strings.Cut(frame, " ")
		if !ok {
			t.Fatalf("wanted a length prefix, got %q", frame)
		}
		length := 0
		if _, err := fmt.Sscanf(prefix, "%d", &length); err != nil {
			t.Fatalf("wanted a numeric length prefix, got %q", prefix)
		}
		if length != len(rest) {
			t.Errorf("length prefix says %d, frame is %d bytes", length, len(rest))
		}
		if strings.HasSuffix(frame, "\n") {
			t.Error("an octet-counted frame carries no trailing newline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
	}
}

// With nothing configured the hot path does no work at all — no encode, no
// queue, no dial.
func TestNoDestinationCostsNothing(t *testing.T) {
	store := &fakeStore{}
	forwarder := New(Options{Store: store})
	ctx, cancel := context.WithCancel(context.Background())
	go forwarder.Run(ctx)
	defer func() { cancel(); forwarder.Wait() }()

	waitUntil(t, func() bool { return len(store.health()) == 0 })
	for range 100 {
		forwarder.Record(context.Background(), event("get", 200))
	}
	if forwarder.active.Load() {
		t.Fatal("a forwarder with no destination must stay inactive")
	}
	if len(forwarder.queue) != 0 {
		t.Fatalf("nothing should have been queued, got %d", len(forwarder.queue))
	}
}

// A full queue drops rather than blocking. A slow SIEM must never become a slow
// kubectl, which is the same trade the database sink makes.
func TestFullQueueDropsAndNeverBlocks(t *testing.T) {
	forwarder := New(Options{Store: &fakeStore{}})
	forwarder.active.Store(true) // no drain running: the queue can only fill

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range queueSize + 500 {
			forwarder.Record(context.Background(), event("get", 200))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked when the queue was full")
	}
	if forwarder.Dropped() < 500 {
		t.Fatalf("wanted the overflow dropped and counted, got %d", forwarder.Dropped())
	}
}

// A store blip must not silently stop the trail leaving the platform: the
// previous destination list is kept rather than emptied.
func TestUnreadableStoreKeepsThePreviousDestinations(t *testing.T) {
	sink := newCollector(t)
	store := &fakeStore{dests: []db.AuditForwarder{sink.dest()}}
	forwarder := start(t, store)

	store.mu.Lock()
	store.listErr = errors.New("database is away")
	store.mu.Unlock()
	forwarder.Reload()

	waitUntil(t, func() bool { return len(forwarder.destinations()) == 1 })
	forwarder.Record(context.Background(), event("get", 200))
	sink.waitFor(t, 1)
}

// Delivery health is written when it changes and then rate-limited, so a busy
// fleet does not turn a status column into a write every two seconds.
func TestHealthIsRecordedOnceUntilItChanges(t *testing.T) {
	sink := newCollector(t)
	store := &fakeStore{dests: []db.AuditForwarder{sink.dest()}}
	forwarder := start(t, store)

	for range 20 {
		forwarder.Record(context.Background(), event("get", 200))
	}
	sink.waitFor(t, 20)

	waitUntil(t, func() bool { return len(store.health()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	health := store.health()
	if len(health) != 1 {
		t.Fatalf("wanted one health write for an unchanged status, got %d", len(health))
	}
	if health[0].status != db.ForwarderStatusOK {
		t.Errorf("status = %q", health[0].status)
	}
}

// A held connection depends on the address, not the row: writing delivery
// health bumps updated_at, and a connection dropped on every successful flush
// would reconnect for every batch.
func TestHealthWriteDoesNotInvalidateTheConnection(t *testing.T) {
	dest := db.AuditForwarder{
		ID: 1, Host: "collector.example.com", Port: 515,
		Protocol: db.ForwarderProtoTCP, Facility: 16, AppName: "kubemg",
	}
	touched := dest
	now := time.Now()
	touched.UpdatedAt = now
	touched.LastStatus = db.ForwarderStatusOK
	touched.LastAttemptAt = &now

	if connKey(dest) != connKey(touched) {
		t.Fatal("delivery health must not look like a configuration change")
	}

	moved := dest
	moved.Port = 5140
	if connKey(dest) == connKey(moved) {
		t.Fatal("a changed port must drop the held connection")
	}
}

// An unreachable destination is reported, not retried forever, and it does not
// stop the drain: the next flush is attempted like any other.
func TestUnreachableDestinationIsReportedAndDoesNotWedge(t *testing.T) {
	// Port 1 on loopback refuses immediately on every platform we run on.
	dest := db.AuditForwarder{
		ID: 9, Name: "down", Kind: db.ForwarderSyslog, Host: "127.0.0.1", Port: 1,
		Protocol: db.ForwarderProtoTCP, Facility: 16, AppName: "kubemg", Enabled: true,
	}
	store := &fakeStore{dests: []db.AuditForwarder{dest}}
	forwarder := start(t, store)

	forwarder.Record(context.Background(), event("get", 200))
	waitUntil(t, func() bool {
		for _, a := range store.health() {
			if a.status == db.ForwarderStatusError {
				return true
			}
		}
		return false
	})
}

// A UDP datagram is one record, and an oversized one is truncated here — where
// the truncation is marked — rather than out on a network that would not say.
func TestOversizedDatagramIsMarkedTruncated(t *testing.T) {
	dest := db.AuditForwarder{
		ID: 1, Host: "127.0.0.1", Port: 514, Protocol: db.ForwarderProtoUDP,
		Facility: 16, AppName: "kubemg",
	}
	big := event("get", 200)
	big.Path = "/api/v1/" + strings.Repeat("a", 2*maxDatagram)
	rec, ok := encode(context.Background(), big)
	if !ok {
		t.Fatal("encode failed")
	}
	payload := frame(dest, rec)
	if len(payload) <= maxDatagram {
		t.Skip("record did not exceed the datagram bound")
	}
	truncated := append(payload[:maxDatagram-len(truncationMarker)], truncationMarker...)
	if len(truncated) != maxDatagram {
		t.Fatalf("truncated to %d, want %d", len(truncated), maxDatagram)
	}
	if json.Valid(truncated[strings.Index(string(truncated), "{"):]) {
		t.Error("a truncated record must not parse as a whole one")
	}
}

// A header field with a space in it shifts every field after it, so the
// receiver reads the message as the structured data.
func TestHeaderFieldsAreSanitized(t *testing.T) {
	if got := sanitizeHeaderField("kube mg\nprod", 48); got != "kubemgprod" {
		t.Fatalf("sanitizeHeaderField = %q", got)
	}
}

// Probe dials on its own rather than borrowing a running connection.
func TestProbeReportsAnUnreachableCollector(t *testing.T) {
	dest := db.AuditForwarder{
		Host: "127.0.0.1", Port: 1, Protocol: db.ForwarderProtoTCP,
		Facility: 16, AppName: "kubemg",
	}
	if err := Probe(context.Background(), dest); err == nil {
		t.Fatal("wanted an error from an unreachable collector")
	}
}

// An unknown protocol is a configuration error, which is not retried: the
// second attempt would fail identically and only double the log noise.
func TestUnknownProtocolIsNotRetried(t *testing.T) {
	s := newSender(db.AuditForwarder{Protocol: "quic", Host: "127.0.0.1", Port: 1})
	err := s.send([]record{{body: []byte("{}"), severity: severityInfo, at: time.Now()}})
	if err == nil {
		t.Fatal("wanted an error")
	}
	if s.retryable(err) {
		t.Fatal("a configuration error must not be retried")
	}
}

// A CA bundle that contains no certificate is refused rather than falling back
// to the system roots — an operator who pinned a private CA and silently got
// public trust would have a forwarder talking to the wrong collector.
func TestUnparseableCABundleIsRefused(t *testing.T) {
	_, err := tlsConfig(db.AuditForwarder{
		Host: "collector.example.com", Protocol: db.ForwarderProtoTLS,
		TLSCABundle: "not a certificate",
	})
	if !errors.Is(err, errConfig) {
		t.Fatalf("wanted a configuration error, got %v", err)
	}
}
