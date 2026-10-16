// Package bastion implements KubeMG's reverse-tunnel gateway: agents deployed
// inside target clusters dial *out* to this server and hold a WebSocket open,
// and every kubectl request KubeMG proxies is framed onto that socket instead
// of being dialled against the cluster's API server.
//
// The wire format lives here rather than in a shared module on purpose: the
// agent is the Apache-2.0 half of KubeMG and compiles as its own Go module, so
// it carries a mirrored copy of these types rather than importing the AGPL
// server. Both sides speak JSON, so the two
// definitions only have to agree on field names, not on Go layout.
package bastion

// ProtocolVersion is bumped when a frame's meaning changes. The server refuses
// a handshake it does not recognise rather than guessing, so bumping this
// requires agents to be upgraded — v2 added the stream frames.
const ProtocolVersion = 2

// MaxSessionMessage is the largest single message an interactive session —
// exec, attach, port-forward — may carry from the client towards the cluster.
// The bastion refuses a larger one and the agent sizes its write buffer to it
// (agent/internal/protocol mirrors this constant),
// so every message the bastion forwards leaves the agent as **one** WebSocket
// frame. That is load-bearing: the API server's exec endpoint reads each frame
// as a message of its own, so a message split into continuation frames arrives
// as one short stdin write followed by frames whose first data byte is taken
// for a channel number, and the rest is silently discarded. The two sides must
// agree on this number; kubectl's own messages are 32 KiB plus a channel byte.
const MaxSessionMessage = 1 << 20

// Capabilities are what one side of the tunnel can do beyond ProtocolVersion's
// floor. They are negotiated rather than versioned on purpose: the agent says
// what it can do in its hello, the server answers with the subset it accepts in
// its welcome, and a side that names nothing gets the v2 behaviour exactly. So
// either half can be upgraded first, and an agent fleet does not have to move in
// lockstep with the server the way a ProtocolVersion bump would force it to.
const (
	// CapabilityResponseParts lets the agent send a response body as a run of
	// response_part frames ahead of the response that ends it, instead of as
	// one frame. See ResponsePart.
	CapabilityResponseParts = "response-parts"
)

// ResponsePartSize is the largest slice of a response body one frame carries
// when the response is sent in parts. A whole 8 MB list in one frame held every
// other session's frames behind it for as long as the link took to carry ~11 MB
// of base64, and past ~10 Mbit/s that was longer than the write deadline — the
// write failed and the tunnel went down for everyone on the cluster. At this
// size the wait an interactive frame can see behind one part is a fraction of a
// second on any link a tunnel is usable over.
const ResponsePartSize = 256 << 10

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
	// MessageResponsePart carries a slice of a response body ahead of the
	// MessageResponse that ends it. Only sent when CapabilityResponseParts was
	// negotiated.
	MessageResponsePart MessageType = "response_part"

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
	Type         MessageType   `json:"type"`
	ID           string        `json:"id,omitempty"`
	Hello        *Hello        `json:"hello,omitempty"`
	Welcome      *Welcome      `json:"welcome,omitempty"`
	Request      *Request      `json:"request,omitempty"`
	Response     *Response     `json:"response,omitempty"`
	ResponsePart *ResponsePart `json:"response_part,omitempty"`
	StreamOpen   *StreamOpen   `json:"stream_open,omitempty"`
	StreamStart  *StreamStart  `json:"stream_start,omitempty"`
	StreamData   *StreamData   `json:"stream_data,omitempty"`
	StreamClose  *StreamClose  `json:"stream_close,omitempty"`
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
	// Capabilities lists what this agent can do beyond the protocol version's
	// floor. An agent that predates them sends none.
	Capabilities []string `json:"capabilities,omitempty"`
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
	// Capabilities is the subset of the hello's that this server accepts. The
	// agent uses nothing it does not find here.
	Capabilities []string `json:"capabilities,omitempty"`
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

// ResponsePart is a slice of a response body, sent ahead of the Response that
// ends it under the same ID. The server joins the parts in arrival order and
// puts the final Response's own Body after them; a final Response carrying Error
// discards whatever parts came before it.
type ResponsePart struct {
	Data []byte `json:"data,omitempty"`
}

// supportedCapabilities is what this server accepts from an agent.
var supportedCapabilities = []string{CapabilityResponseParts}

// negotiateCapabilities answers a hello's capabilities with the ones this
// server accepts, in the agent's order. An unknown capability is not an error:
// a newer agent offering something this server has never heard of simply does
// not get it.
func negotiateCapabilities(offered []string) []string {
	var out []string
	for _, capability := range offered {
		for _, supported := range supportedCapabilities {
			if capability == supported {
				out = append(out, capability)
				break
			}
		}
	}
	return out
}
