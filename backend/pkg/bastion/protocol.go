// Package bastion implements KubeMG's reverse-tunnel gateway: agents deployed
// inside target clusters dial *out* to this server and hold a WebSocket open,
// and every kubectl request KubeMG proxies is framed onto that socket instead
// of being dialled against the cluster's API server.
//
// The wire format lives here rather than in a shared module on purpose: the
// agent is the open-source half of KubeMG and compiles as its own Go module, so
// it carries a mirrored copy of these types. Both sides speak JSON, so the two
// definitions only have to agree on field names, not on Go layout.
package bastion

// ProtocolVersion is bumped when a frame's meaning changes. The server refuses
// a handshake it does not recognise rather than guessing, so bumping this
// requires agents to be upgraded — v2 added the stream frames.
const ProtocolVersion = 2

// MessageType discriminates the frames multiplexed over one tunnel.
type MessageType string

const (
	// MessageHello is the agent's opening frame, sent once per connection.
	MessageHello MessageType = "hello"
	// MessageWelcome is the server's acknowledgement of a hello.
	MessageWelcome MessageType = "welcome"
	// MessageRequest carries a proxied API request from server to agent.
	MessageRequest MessageType = "request"
	// MessageResponse carries the target API server's answer back.
	MessageResponse MessageType = "response"

	// The frames below carry long-lived calls — watch, logs -f, exec — that a
	// single request/response pair cannot express. A stream is identified by
	// the envelope's ID for its whole life.

	// MessageStreamOpen asks the agent to start a streaming call.
	MessageStreamOpen MessageType = "stream_open"
	// MessageStreamStart is the agent's answer: the response head, or why
	// there is not going to be one.
	MessageStreamStart MessageType = "stream_start"
	// MessageStreamData is a chunk, in either direction. Bidirectional streams
	// (exec, attach) use it both ways; a watch only ever flows agent to server.
	MessageStreamData MessageType = "stream_data"
	// MessageStreamClose ends a stream from either side.
	MessageStreamClose MessageType = "stream_close"
)

// Message is the single envelope on the wire. Exactly one payload field is set,
// selected by Type. ID correlates a response with its request — and a stream's
// frames with each other — and is empty on handshake frames.
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
	// Upgrade asks the agent to negotiate a protocol upgrade rather than read
	// a response body — this is what exec and attach need.
	Upgrade bool `json:"upgrade,omitempty"`
	// Subprotocols are offered during that upgrade. For exec these are the
	// Kubernetes channel protocols (v4.channel.k8s.io and friends).
	Subprotocols []string `json:"subprotocols,omitempty"`
}

// StreamStart is the response head. A non-empty Error means the stream never
// opened and no data or close frame follows.
type StreamStart struct {
	Status      int                 `json:"status"`
	Header      map[string][]string `json:"header,omitempty"`
	Subprotocol string              `json:"subprotocol,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// StreamData is one chunk of a stream.
type StreamData struct {
	Data []byte `json:"data,omitempty"`
	// Binary marks a chunk that must be replayed as a binary WebSocket message
	// rather than text. The Kubernetes exec channel protocol is binary, and
	// mislabelling it silently corrupts the session.
	Binary bool `json:"binary,omitempty"`
}

// StreamClose ends a stream. Error explains an abnormal end.
type StreamClose struct {
	Error string `json:"error,omitempty"`
}

// Hello is what an agent announces itself with. The cluster identity is not in
// here: it is derived from the bearer token on the upgrade request, so an agent
// cannot claim to be a cluster it holds no token for.
type Hello struct {
	ProtocolVersion int `json:"protocol_version"`
	// AgentVersion is the agent build, surfaced in the cluster inventory so an
	// operator can see which clusters are running an old agent.
	AgentVersion string `json:"agent_version"`
	// KubernetesVersion is what the agent read from its own API server, which
	// saves KubeMG a round trip to display it.
	KubernetesVersion string `json:"kubernetes_version,omitempty"`
}

// Welcome confirms the handshake and tells the agent which cluster it bound to,
// which is the only way the agent learns its own registered name.
type Welcome struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClusterID       uint   `json:"cluster_id"`
	ClusterName     string `json:"cluster_name"`
	// HeartbeatSeconds is how often the server expects a pong. The agent uses
	// it to size its own keepalive so idle load balancers do not cut the tunnel.
	HeartbeatSeconds int `json:"heartbeat_seconds"`
}

// Request is one HTTP call to replay against the in-cluster API server. Path
// carries the query string, because the agent rebuilds the URL verbatim.
type Request struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// Response is the API server's answer, or the agent's explanation of why there
// isn't one. A non-empty Error means Status carries no meaning.
type Response struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
	Error  string              `json:"error,omitempty"`
}
