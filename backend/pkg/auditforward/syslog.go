package auditforward

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * RFC 5424 with a JSON message.
 *
 * The header is what a collector routes on — facility, severity, timestamp,
 * hostname, app name — and the message is the record itself as JSON, which is
 * what its parser reads. Both halves matter: a SIEM that only understood the
 * JSON would have nothing to filter this stream by before parsing it, and one
 * that only understood the header would know a kubemg record arrived and
 * nothing about what it said.
 *
 * Structured data is `-` rather than an SD-ELEMENT duplicating the JSON. Sending
 * every field twice doubles a record that is already the largest thing syslog
 * carries here, and no receiver would agree which copy wins.
 */

const (
	// syslogVersion is RFC 5424's version field. There is exactly one.
	syslogVersion = "1"
	// nilValue is RFC 5424's "this field has no value".
	nilValue = "-"

	dialTimeout  = 5 * time.Second
	writeTimeout = 5 * time.Second

	// maxDatagram bounds a UDP frame. RFC 5426 only guarantees 480 bytes and
	// most collectors take far more, but past the path MTU a datagram is
	// fragmented or dropped by something that will not tell us — so an
	// oversized record is truncated here, where the truncation can at least be
	// marked, rather than out on the network where it cannot.
	maxDatagram = 8192
)

// truncationMarker replaces the tail of an oversized datagram. It is inside the
// JSON string it truncates, which makes the record unparseable — deliberately:
// a SIEM must not silently index a record whose fields were cut off mid-value
// as though it were the whole truth.
const truncationMarker = `…[kubemg: truncated]`

// hostname is resolved once. A failure yields RFC 5424's nil value rather than
// an error: not knowing which host sent a record is a much smaller problem than
// not sending it.
var hostname = sync.OnceValue(func() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return nilValue
	}
	return sanitizeHeaderField(name, 255)
})

// frame renders one record as an RFC 5424 message for the given destination.
func frame(dest db.AuditForwarder, rec record) []byte {
	facility := dest.Facility
	if facility < 0 || facility > 23 {
		facility = db.DefaultForwarderFacility
	}
	appName := sanitizeHeaderField(dest.AppName, 48)
	if appName == "" {
		appName = db.DefaultForwarderAppName
	}

	priority := facility*8 + rec.severity

	var b strings.Builder
	fmt.Fprintf(&b, "<%d>%s %s %s %s %s %s %s ",
		priority,
		syslogVersion,
		rec.at.Format(time.RFC3339Nano),
		hostname(),
		appName,
		nilValue, // PROCID: a replica id would change on every restart
		"kubemg-audit",
		nilValue, // STRUCTURED-DATA
	)
	b.Write(rec.body)
	return []byte(b.String())
}

// sanitizeHeaderField keeps a header field to RFC 5424's printable US-ASCII,
// with no spaces — a space in APP-NAME shifts every field after it, so the
// receiver reads the message as the structured data and the record as garbage.
func sanitizeHeaderField(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 33 || r > 126 {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= max {
			break
		}
	}
	return b.String()
}

// sender delivers framed records to one destination. A stream sender holds its
// connection open across flushes — a syslog receiver expects a long-lived
// connection, and reconnecting per record is how a busy fleet exhausts the
// receiver's accept queue.
type sender struct {
	dest db.AuditForwarder
	conn net.Conn
}

func newSender(dest db.AuditForwarder) *sender { return &sender{dest: dest} }

// send delivers a batch, reconnecting once if the held connection has gone
// away. One retry rather than none because the common failure is a receiver
// that closed an idle connection between flushes, and one retry rather than
// many because the caller is a queue that must keep draining.
func (s *sender) send(batch []record) error {
	if err := s.deliver(batch); err == nil {
		return nil
	} else if !s.retryable(err) {
		return err
	}
	s.close()
	return s.deliver(batch)
}

// retryable reports whether reconnecting could plausibly help. A configuration
// error — an unparseable CA bundle, an unknown protocol — will fail identically
// on the second attempt, and retrying it only doubles the log noise.
func (s *sender) retryable(err error) bool { return !errors.Is(err, errConfig) }

var errConfig = errors.New("forwarder configuration")

func (s *sender) deliver(batch []record) error {
	if s.conn == nil {
		conn, err := dial(s.dest)
		if err != nil {
			return err
		}
		s.conn = conn
	}

	deadline := time.Now().Add(writeTimeout)
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	datagram := s.dest.Protocol == db.ForwarderProtoUDP
	for _, rec := range batch {
		payload := frame(s.dest, rec)
		if datagram {
			// One record per datagram: a datagram is the message boundary, so
			// batching several into one would deliver a single unparseable blob.
			if len(payload) > maxDatagram {
				payload = append(payload[:maxDatagram-len(truncationMarker)], truncationMarker...)
			}
			if _, err := s.conn.Write(payload); err != nil {
				return err
			}
			continue
		}
		if _, err := s.conn.Write(streamFrame(s.dest, payload)); err != nil {
			return err
		}
	}
	return nil
}

// streamFrame applies the transport framing a stream needs, because a stream has
// no message boundary of its own.
func streamFrame(dest db.AuditForwarder, payload []byte) []byte {
	if dest.OctetCounting {
		// RFC 6587 octet counting: the length, a space, then exactly that many
		// bytes. Unambiguous, and the only framing that survives a message
		// containing a newline.
		return append([]byte(fmt.Sprintf("%d ", len(payload))), payload...)
	}
	return append(payload, '\n')
}

func (s *sender) close() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// dial opens the destination's transport.
func dial(dest db.AuditForwarder) (net.Conn, error) {
	address := dest.Address()
	switch dest.Protocol {
	case db.ForwarderProtoUDP:
		return net.DialTimeout("udp", address, dialTimeout)
	case db.ForwarderProtoTCP:
		return net.DialTimeout("tcp", address, dialTimeout)
	case db.ForwarderProtoTLS:
		cfg, err := tlsConfig(dest)
		if err != nil {
			return nil, err
		}
		return tls.DialWithDialer(&net.Dialer{Timeout: dialTimeout}, "tcp", address, cfg)
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", errConfig, dest.Protocol)
	}
}

func tlsConfig(dest db.AuditForwarder) (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName:         dest.Host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: dest.TLSInsecureSkipVerify, //nolint:gosec // operator's explicit choice, see the model
	}
	bundle := strings.TrimSpace(dest.TLSCABundle)
	if bundle == "" {
		return cfg, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle)) {
		// Refused rather than quietly falling back to the system roots: an
		// operator who pinned a private CA and got public trust instead would
		// have a forwarder that works right up until it is talking to the wrong
		// collector.
		return nil, fmt.Errorf("%w: ca bundle contains no certificate", errConfig)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
