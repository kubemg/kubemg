package kube

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// frameHead is the part of a raw WebSocket frame header this test cares about.
type frameHead struct {
	fin    bool
	length uint64
}

// readFrameHead parses one frame header straight off the socket. Reading through
// gorilla would reassemble continuation frames and hide the very thing under
// test.
func readFrameHead(r io.Reader) (frameHead, error) {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return frameHead{}, err
	}
	head := frameHead{fin: b[0]&0x80 != 0, length: uint64(b[1] & 0x7f)}
	switch head.length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frameHead{}, err
		}
		head.length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frameHead{}, err
		}
		head.length = binary.BigEndian.Uint64(ext[:])
	}
	return head, nil
}

// The API server's exec endpoint reads each frame as a message, so a stdin
// write the agent split into fragments reached the container as its first 4095
// bytes and nothing else. The largest message the bastion forwards has to leave
// as one final frame.
func TestDialUpgradeSendsTheLargestSessionMessageAsOneFrame(t *testing.T) {
	heads := make(chan frameHead, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		head, err := readFrameHead(conn.UnderlyingConn())
		if err != nil {
			t.Errorf("read frame header: %v", err)
			return
		}
		heads <- head
		// Keep reading until the client hangs up. Closing here, with most of a
		// megabyte still in flight, resets the client's write under it.
		_, _ = io.Copy(io.Discard, conn.UnderlyingConn())
	}))
	defer server.Close()

	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{APIURL: server.URL, TokenPath: token, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}

	conn, _, err := client.DialUpgrade(context.Background(),
		"/api/v1/namespaces/a/pods/b/exec", nil, []string{"v5.channel.k8s.io"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	message := make([]byte, protocol.MaxSessionMessage) // channel 0, stdin
	if err := conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case head := <-heads:
		if !head.fin || head.length != uint64(len(message)) {
			t.Fatalf("first frame fin=%t length=%d; want one final frame of %d bytes",
				head.fin, head.length, len(message))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
	}
}
