package bastion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// The Kubernetes exec/attach channel protocol prefixes every frame with the
// channel it belongs to. The proxy pipes those bytes verbatim — it has to, the
// framing is the API server's — so recording a session means reading the prefix
// without disturbing the payload.
const (
	channelStdin  = 0
	channelStdout = 1
	channelStderr = 2
	channelError  = 3
	channelResize = 4
)

// SessionMeta describes an interactive session about to be recorded. It is what
// the recording is filed under, and it is derived entirely from the request the
// proxy already resolved — nothing here is taken from the client.
type SessionMeta struct {
	// SessionID also travels on this session's audit records, which is what ties
	// a line in the trail to the recording of it.
	SessionID string
	UserID    uint
	Username  string
	ClusterID uint
	Cluster   string
	Namespace string
	Pod       string
	Container string
	// Shell is the argv the caller asked to run, joined for display. An attach
	// has none: it joins whatever the container is already running.
	Shell string
	// Verb is "exec" or "attach".
	Verb     string
	At       time.Time
	TTY      bool
	HasStdin bool
}

// SessionRecorder opens a recording for an interactive session. It is a hook
// rather than something the proxy does itself because where recordings live and
// what format they take is not a gateway concern; see pkg/terminal.
//
// Begin returns nil when the session is not being recorded — recording is
// optional, and a proxy that refused to open a shell because a disk was full
// would be a worse gateway than one that logs the gap.
type SessionRecorder interface {
	Begin(ctx context.Context, meta SessionMeta) SessionSink
}

// SessionSink accepts one session's traffic. Both directions are live at once,
// so implementations must be safe from more than one goroutine.
type SessionSink interface {
	// Output records bytes the container produced.
	Output(data []byte)
	// Input records bytes the operator typed. This is the half that makes a
	// recording an audit artefact rather than a screencast, and the half that can
	// capture a secret somebody pasted into a shell — a recording is as sensitive
	// as the session it holds.
	Input(data []byte)
	// Resize records a terminal geometry change, so a replay reflows the way the
	// operator's window did.
	Resize(cols, rows int)
	// Close finishes the recording, carrying whatever ended the session.
	Close(cause error)
}

// newSessionID mints the correlation id a session's audit records and its
// recording share.
func newSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail in practice, and a session with no id is
		// still a session worth carrying — it simply cannot be correlated.
		return ""
	}
	return hex.EncodeToString(raw[:])
}

// beginRecording opens a recording for this stream, or returns nil when there is
// nothing to record. Only exec and attach qualify: a port-forward carries
// arbitrary TCP, which is not a terminal and would be meaningless to replay.
func (p *Proxy) beginRecording(ctx context.Context, event *Event, parsed APIPath) SessionSink {
	if p.recorder == nil || event.SessionID == "" {
		return nil
	}
	// The runtime switch. It sits here rather than inside the recorder because
	// this is the one place that knows a session is starting: flipping the setting
	// stops the next shell from being recorded and leaves the ones already running
	// alone, which is the only behaviour that does not produce a recording with a
	// hole in the middle of it.
	if p.policy != nil && !p.policy.RecordSessions() {
		return nil
	}
	switch parsed.Subresource {
	case "exec", "attach":
	default:
		return nil
	}

	query := queryOf(event.Path)
	return p.recorder.Begin(ctx, SessionMeta{
		SessionID: event.SessionID,
		UserID:    event.UserID,
		Username:  event.Username,
		ClusterID: event.ClusterID,
		Cluster:   event.Cluster,
		Namespace: parsed.Namespace,
		Pod:       parsed.Name,
		Container: query.Get("container"),
		Shell:     strings.Join(query["command"], " "),
		Verb:      parsed.Subresource,
		At:        event.At,
		TTY:       query.Get("tty") == "true",
		HasStdin:  query.Get("stdin") == "true",
	})
}

// queryOf pulls the query string off a proxied path. The path has already had
// KubeMG's own parameters stripped by the time it lands in an Event.
func queryOf(path string) url.Values {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return url.Values{}
	}
	values, err := url.ParseQuery(path[i+1:])
	if err != nil {
		return url.Values{}
	}
	return values
}

// recordFromCluster files a frame the cluster sent. stdout and stderr are one
// stream in a replay, exactly as they are on a terminal; the error channel is
// the API server talking about the session rather than the session itself, so it
// is left out of the recording.
func recordFromCluster(sink SessionSink, frame []byte) {
	if sink == nil || len(frame) < 2 {
		return
	}
	switch frame[0] {
	case channelStdout, channelStderr:
		sink.Output(frame[1:])
	}
}

// recordFromClient files a frame the operator sent: keystrokes on stdin, and
// window geometry on the resize channel.
func recordFromClient(sink SessionSink, frame []byte) {
	if sink == nil || len(frame) < 2 {
		return
	}
	switch frame[0] {
	case channelStdin:
		sink.Input(frame[1:])
	case channelResize:
		if cols, rows, ok := decodeResize(frame[1:]); ok {
			sink.Resize(cols, rows)
		}
	}
}

// decodeResize reads the API server's terminal size message. The field names are
// capitalised because that is what client-go writes on the wire.
func decodeResize(payload []byte) (int, int, bool) {
	var size struct {
		Width  int `json:"Width"`
		Height int `json:"Height"`
	}
	if err := json.Unmarshal(payload, &size); err != nil {
		return 0, 0, false
	}
	if size.Width <= 0 || size.Height <= 0 {
		return 0, 0, false
	}
	return size.Width, size.Height, true
}
