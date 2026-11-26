package bastion

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
)

// BenchmarkStreamDataFrame is the measurement behind CapabilityBinaryStreamData:
// one chunk encoded by the sender and decoded by the receiver, as a JSON
// envelope and as a binary frame, at a keystroke-sized, a watch-event-sized and
// the agent's chunkSize payload. wire/payload is how many bytes go on the wire
// per byte of the chunk.
func BenchmarkStreamDataFrame(b *testing.B) {
	id := "s12-34567"
	for _, size := range []int{512, 4 << 10, 32 << 10} {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		msg := Message{Type: MessageStreamData, ID: id, StreamData: &StreamData{Data: data, Binary: true}}

		b.Run(fmt.Sprintf("json/%d", size), func(b *testing.B) {
			frame, _ := json.Marshal(msg)
			b.ReportMetric(float64(len(frame))/float64(size), "wire/payload")
			b.SetBytes(int64(size))
			for b.Loop() {
				frame, _ := json.Marshal(msg)
				var back Message
				_ = json.Unmarshal(frame, &back)
			}
		})
		b.Run(fmt.Sprintf("binary/%d", size), func(b *testing.B) {
			frame, _ := encodeStreamData(id, *msg.StreamData)
			b.ReportMetric(float64(len(frame))/float64(size), "wire/payload")
			b.SetBytes(int64(size))
			for b.Loop() {
				frame, _ := encodeStreamData(id, *msg.StreamData)
				_, _, _ = decodeStreamData(frame)
			}
		})
	}
}
