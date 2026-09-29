// Package protocol is the KubeMG tunnel wire format.
//
// It mirrors backend/pkg/bastion/protocol.go. The duplication is deliberate:
// the agent is the Apache-2.0 half of KubeMG and ships as its own Go module, so
// it does not import the AGPL server. Both sides speak JSON, so the
// two copies only have to agree on field names and on ProtocolVersion — which
// the server checks during the handshake and refuses when it disagrees.
package protocol

// ProtocolVersion must match the server's. v2 added the stream frames, and the
// bastion refuses a handshake at any other version — so an agent older than
// this cannot half-work against a newer server.
const ProtocolVersion = 2

// MaxSessionMessage is the largest single message an interactive session —
// exec, attach, port-forward — may carry from the client towards the cluster.
// The bastion refuses a larger one and the agent sizes its write buffer to it,
// so every message the bastion forwards leaves the agent as **one** WebSocket
// frame. That is load-bearing: the API server's exec endpoint reads each frame
// as a message of its own, so a message split into continuation frames arrives
// as one short stdin write followed by frames whose first data byte is taken
// for a channel number, and the rest is silently discarded. The two sides must
// agree on this number; kubectl's own messages are 32 KiB plus a channel byte.
const MaxSessionMessage = 1 << 20

// MessageType discriminates the frames multiplexed over one tunnel.
type MessageType string

const (
	MessageHello    MessageType = "hello"
	MessageWelcome  MessageType = "welcome"
	MessageRequest  MessageType = "request"
	MessageResponse MessageType = "response"

	// Long-lived calls — watch, logs -f, exec — that a single request/response
	// pair cannot express. A stream is identified by the envelope's ID.
	MessageStreamOpen  MessageType = "stream_open"
	MessageStreamStart MessageType = "stream_start"
	MessageStreamData  MessageType = "stream_data"
	MessageStreamClose MessageType = "stream_close"
)

// Message is the single envelope on the wire. Exactly one payload field is set,
// selected by Type; ID correlates a response with its request, and a stream's
// frames with each other.
type Message struct {
	Type        MessageType  `json:"type"`
	ID          string       `json:"id,omitempty"`
	Hello       *Hello       `json:"hello,omitempty"`
	Welcome     *Welcome     `json:"welcome,omitempty"`
	Request     *Request     `json:"request,omitempty"`
	Response    *Response    `json:"response,omitempty"`
	StreamOpen  *StreamOpen  `json:"stream_open,omitempty"`
	StreamStart *StreamStart `json:"stream_start,omitempty"`
	StreamData  *StreamData  `json:"stream_data,omitempty"`
	StreamClose *StreamClose `json:"stream_close,omitempty"`
}

// StreamOpen starts a streaming call against the target API server.
type StreamOpen struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	// Upgrade asks for a protocol upgrade rather than a response body — what
	// exec and attach need.
	Upgrade bool `json:"upgrade,omitempty"`
	// Subprotocols offered during that upgrade, i.e. the Kubernetes channel
	// protocols.
	Subprotocols []string `json:"subprotocols,omitempty"`
}

// StreamStart is the response head. A non-empty Error means the stream never
// opened and no further frames follow.
type StreamStart struct {
	Status      int                 `json:"status"`
	Header      map[string][]string `json:"header,omitempty"`
	Subprotocol string              `json:"subprotocol,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// StreamData is one chunk of a stream.
type StreamData struct {
	Data []byte `json:"data,omitempty"`
	// Binary marks a chunk that must be replayed as a binary WebSocket message.
	// The Kubernetes exec channel protocol is binary, and mislabelling it
	// silently corrupts the session.
	Binary bool `json:"binary,omitempty"`
}

// StreamClose ends a stream. Error explains an abnormal end.
type StreamClose struct {
	Error string `json:"error,omitempty"`
}

// Hello is the agent's opening frame. It carries no cluster identity: the
// server derives that from the registration token on the upgrade request.
type Hello struct {
	ProtocolVersion   int    `json:"protocol_version"`
	AgentVersion      string `json:"agent_version"`
	KubernetesVersion string `json:"kubernetes_version,omitempty"`
}

// Welcome confirms the handshake and names the cluster the tunnel bound to.
type Welcome struct {
	ProtocolVersion  int    `json:"protocol_version"`
	ClusterID        uint   `json:"cluster_id"`
	ClusterName      string `json:"cluster_name"`
	HeartbeatSeconds int    `json:"heartbeat_seconds"`
}

// Request is one HTTP call to replay against the in-cluster API server. Path
// includes the query string and is rebuilt verbatim.
type Request struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// Response is the API server's answer, or an explanation of why there isn't
// one. A non-empty Error means Status carries no meaning.
type Response struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
	Error  string              `json:"error,omitempty"`
}
