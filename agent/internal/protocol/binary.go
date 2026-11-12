package protocol

import "errors"

// A stream_data frame, when CapabilityBinaryStreamData is negotiated, is one
// binary WebSocket message:
//
//	byte 0      frame type, binaryStreamData
//	byte 1      flags: binaryFlagBinary replays the chunk as a binary message
//	byte 2      length of the stream ID, 1 to 255
//	3..3+n      the stream ID
//	the rest    the chunk, to the end of the message
//
// The message's own length delimits the chunk, so the header carries none.
// It mirrors backend/pkg/bastion/binaryframe.go; both sides test it against the
// same bytes.
const (
	binaryStreamData byte = 0x01
	binaryFlagBinary byte = 1 << 0
)

// ErrMalformedBinaryFrame is a binary message that is not a stream_data frame
// this side can read.
var ErrMalformedBinaryFrame = errors.New("not a stream_data frame")

// EncodeStreamData renders a chunk as a binary frame. It copies the chunk, so
// the caller may reuse its buffer once this returns. An ID that does not fit
// the header reports false, and the frame goes as JSON instead.
func EncodeStreamData(id string, chunk StreamData) ([]byte, bool) {
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

// DecodeStreamData reads a binary frame. The chunk is a slice of frame, not a
// copy: gorilla hands every message its own buffer. An unknown flag is refused
// rather than ignored, since a flag a later version adds may change what the
// bytes mean.
func DecodeStreamData(frame []byte) (string, StreamData, error) {
	if len(frame) < 3 || frame[0] != binaryStreamData || frame[1]&^binaryFlagBinary != 0 {
		return "", StreamData{}, ErrMalformedBinaryFrame
	}
	n := int(frame[2])
	if n == 0 || len(frame) < 3+n {
		return "", StreamData{}, ErrMalformedBinaryFrame
	}
	chunk := StreamData{Binary: frame[1]&binaryFlagBinary != 0}
	if data := frame[3+n:]; len(data) > 0 {
		chunk.Data = data
	}
	return string(frame[3 : 3+n]), chunk, nil
}

// IsBinaryFrame reports whether an encoded frame goes out as a binary message.
// A JSON frame always opens with '{', and a binary frame with its frame type,
// so the frame says which it is and the writer's queues carry nothing beside it.
func IsBinaryFrame(payload []byte) bool {
	return len(payload) > 0 && payload[0] == binaryStreamData
}
