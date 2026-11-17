package bastion

import (
	"errors"

	"github.com/gorilla/websocket"
)

// A stream_data frame, when CapabilityBinaryStreamData is negotiated, is one
// binary WebSocket message:
//
//	byte 0      frame type, binaryStreamData
//	byte 1      flags: binaryFlagBinary replays the chunk as a binary message
//	byte 2      length of the stream ID, 1 to 255
//	3..3+n      the stream ID
//	the rest    the chunk, to the end of the message
//
// The message's own length delimits the chunk, so the header carries none. The
// JSON envelope base64-encodes a chunk: a third more bytes on the wire, an
// encode on the sender and a decode and a copy on the receiver, for every byte
// of exec, attach, logs -f and watch output. This carries the bytes as they are.
//
// agent/internal/protocol mirrors this format; both sides test it against the
// same bytes.
const (
	binaryStreamData byte = 0x01
	binaryFlagBinary byte = 1 << 0
)

// errMalformedBinaryFrame is a binary message that is not a stream_data frame
// this side can read.
var errMalformedBinaryFrame = errors.New("not a stream_data frame")

// encodeStreamData renders a chunk as a binary frame. It copies the chunk, so
// the caller may reuse its buffer once this returns. An ID that does not fit
// the header reports false, and the frame goes as JSON instead.
func encodeStreamData(id string, chunk StreamData) ([]byte, bool) {
	if id == "" || len(id) > 255 {
		return nil, false
	}
	frame := make([]byte, 3+len(id)+len(chunk.Data))
	frame[0] = binaryStreamData
	if chunk.Binary {
		frame[1] = binaryFlagBinary
	}
	frame[2] = byte(len(id))
	copy(frame[3:], id)
	copy(frame[3+len(id):], chunk.Data)
	return frame, true
}

// decodeStreamData reads a binary frame. The chunk is a slice of frame, not a
// copy: gorilla hands every message its own buffer, so nothing else holds it.
// An unknown flag is refused rather than ignored, since a flag a later version
// adds may change what the bytes mean.
func decodeStreamData(frame []byte) (string, StreamData, error) {
	if len(frame) < 3 || frame[0] != binaryStreamData || frame[1]&^binaryFlagBinary != 0 {
		return "", StreamData{}, errMalformedBinaryFrame
	}
	n := int(frame[2])
	if n == 0 || len(frame) < 3+n {
		return "", StreamData{}, errMalformedBinaryFrame
	}
	chunk := StreamData{Binary: frame[1]&binaryFlagBinary != 0}
	if data := frame[3+n:]; len(data) > 0 {
		chunk.Data = data
	}
	return string(frame[3 : 3+n]), chunk, nil
}

// wireKind is the WebSocket message type a queued frame goes out as. A JSON
// frame always opens with '{', and a binary frame with its frame type, so the
// frame says which it is and the queues carry nothing beside it.
func wireKind(payload []byte) int {
	if len(payload) > 0 && payload[0] == binaryStreamData {
		return websocket.BinaryMessage
	}
	return websocket.TextMessage
}
