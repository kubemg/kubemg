package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// streamDataVector is the frame both sides of the tunnel test against:
// backend/pkg/bastion/binaryframe_test.go holds the same bytes.
var streamDataVector = []byte{0x01, 0x01, 0x04, '7', '-', '4', '2', 0x00, 'h', 'i'}

func TestStreamDataFrameMatchesTheBastions(t *testing.T) {
	frame, ok := EncodeStreamData("7-42", StreamData{Data: []byte("\x00hi"), Binary: true})
	if !ok || !bytes.Equal(frame, streamDataVector) {
		t.Fatalf("frame = %v, want %v", frame, streamDataVector)
	}
	id, chunk, err := DecodeStreamData(streamDataVector)
	if err != nil || id != "7-42" || !chunk.Binary || string(chunk.Data) != "\x00hi" {
		t.Fatalf("decoded %q %+v %v", id, chunk, err)
	}
}

func TestStreamDataFrameRoundTrips(t *testing.T) {
	for _, chunk := range []StreamData{
		{Data: []byte("text output\n")},
		{Data: bytes.Repeat([]byte{0xff, 0x00}, 32<<10), Binary: true},
		{},
	} {
		frame, ok := EncodeStreamData("12-3456789", chunk)
		if !ok {
			t.Fatal("a short ID did not fit")
		}
		id, back, err := DecodeStreamData(frame)
		if err != nil || id != "12-3456789" || back.Binary != chunk.Binary || !bytes.Equal(back.Data, chunk.Data) {
			t.Fatalf("round trip of %d bytes: %q %+v %v", len(chunk.Data), id, back.Binary, err)
		}
	}
}

func TestStreamDataFrameCopiesTheChunk(t *testing.T) {
	buf := []byte("first")
	frame, _ := EncodeStreamData("1-1", StreamData{Data: buf})
	copy(buf, "XXXXX")
	if _, chunk, _ := DecodeStreamData(frame); string(chunk.Data) != "first" {
		t.Fatalf("reusing the buffer changed the queued frame to %q", chunk.Data)
	}
}

func TestAnIDThatDoesNotFitFallsBackToJSON(t *testing.T) {
	if _, ok := EncodeStreamData("", StreamData{}); ok {
		t.Fatal("an empty ID was encoded")
	}
	if _, ok := EncodeStreamData(string(bytes.Repeat([]byte("x"), 256)), StreamData{}); ok {
		t.Fatal("a 256-byte ID was encoded")
	}
}

func TestMalformedStreamDataFramesAreRefused(t *testing.T) {
	for name, frame := range map[string][]byte{
		"empty":          {},
		"header only":    {0x01, 0x00},
		"wrong type":     {0x02, 0x00, 0x01, 'a'},
		"unknown flag":   {0x01, 0x02, 0x01, 'a'},
		"no ID":          {0x01, 0x00, 0x00, 'a'},
		"truncated ID":   {0x01, 0x00, 0x05, 'a', 'b'},
		"a JSON message": []byte(`{"type":"stream_data"}`),
	} {
		if _, _, err := DecodeStreamData(frame); !errors.Is(err, ErrMalformedBinaryFrame) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOnlyBinaryFramesGoOutAsBinary(t *testing.T) {
	frame, _ := EncodeStreamData("1-1", StreamData{Data: []byte("x")})
	if !IsBinaryFrame(frame) {
		t.Fatal("a binary frame would go out as text")
	}
	payload, _ := json.Marshal(Message{Type: MessageStreamData, ID: "1-1", StreamData: &StreamData{Data: []byte("x")}})
	if IsBinaryFrame(payload) {
		t.Fatal("a JSON frame would go out as binary")
	}
}
