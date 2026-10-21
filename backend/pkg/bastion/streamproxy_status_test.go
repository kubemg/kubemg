package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/exec"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// execSession dials a kubectl-shaped exec through the harness, against an
// agent that answers the upgrade on the given subprotocol and then runs then.
func execSession(t *testing.T, subresource, subprotocol string, then func(a *fakeAgent, id string)) (*harness, *fakeAgent, *websocket.Conn) {
	t.Helper()
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	session := func(a *fakeAgent, id string, _ StreamOpen) {
		_ = a.send(Message{Type: MessageStreamStart, ID: id, StreamStart: &StreamStart{
			Status: http.StatusSwitchingProtocols, Subprotocol: subprotocol,
		}})
		if then != nil {
			then(a, id)
		}
	}
	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), session)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	url := "ws" + strings.TrimPrefix(h.server.URL, "http") +
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/" + subresource + "?command=sh&stdin=true"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+h.token(admin))
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{subprotocol}
	conn, _, err := dialer.Dial(url, header)
	if err != nil {
		t.Fatalf("exec upgrade failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return h, agent, conn
}

// readUntilClose collects every message until the session's close frame.
func readUntilClose(t *testing.T, conn *websocket.Conn) ([][]byte, *websocket.CloseError) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frames [][]byte
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("session ended with %v, want a close frame", err)
			}
			return frames, closeErr
		}
		frames = append(frames, payload)
	}
}

func statusFrames(frames [][]byte) []map[string]any {
	var out []map[string]any
	for _, frame := range frames {
		if len(frame) > 0 && frame[0] == channelError {
			var status map[string]any
			if json.Unmarshal(frame[1:], &status) == nil {
				out = append(out, status)
			}
		}
	}
	return out
}

// The case the roadmap found: the tunnel goes away mid-session. kubectl takes
// its exit code from the error channel, so a Failure has to be there, and the
// close behind it is a normal one so that client-go reads it.
func TestATunnelDropEndsAnExecWithAFailureStatus(t *testing.T) {
	for _, subprotocol := range []string{"v5.channel.k8s.io", "v4.channel.k8s.io"} {
		t.Run(subprotocol, func(t *testing.T) {
			_, agent, conn := execSession(t, "exec", subprotocol, func(a *fakeAgent, id string) {
				_ = a.send(Message{Type: MessageStreamData, ID: id,
					StreamData: &StreamData{Data: []byte{1, '$', ' '}, Binary: true}})
			})
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatalf("read prompt: %v", err)
			}
			agent.close()

			frames, closeErr := readUntilClose(t, conn)
			statuses := statusFrames(frames)
			if len(statuses) != 1 {
				t.Fatalf("got %d error-channel statuses, want 1 (frames %q)", len(statuses), frames)
			}
			if statuses[0]["status"] != "Failure" || statuses[0]["kind"] != "Status" ||
				!strings.Contains(statuses[0]["message"].(string), "kubemg lost the session") {
				t.Fatalf("status = %v, want a Failure naming the lost session", statuses[0])
			}
			if closeErr.Code != websocket.CloseNormalClosure {
				t.Fatalf("close code = %d, want %d — a 1011 behind the Status fails client-go's read of it",
					closeErr.Code, websocket.CloseNormalClosure)
			}
		})
	}
}

// A command that finished carries the cluster's own Status; nothing is added,
// and the close is a clean one.
func TestAFinishedExecCarriesOnlyTheClustersStatus(t *testing.T) {
	_, _, conn := execSession(t, "exec", "v5.channel.k8s.io", func(a *fakeAgent, id string) {
		_ = a.send(Message{Type: MessageStreamData, ID: id,
			StreamData: &StreamData{Data: append([]byte{channelError}, `{"metadata":{},"status":"Success"}`...), Binary: true}})
		_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
	})
	frames, closeErr := readUntilClose(t, conn)
	statuses := statusFrames(frames)
	if len(statuses) != 1 || statuses[0]["status"] != "Success" {
		t.Fatalf("statuses = %v, want the cluster's Success alone", statuses)
	}
	if closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseNormalClosure)
	}
}

// A Status already relayed is the answer; a second one would make client-go's
// decoder read two objects and report a garbled error instead of the real one.
func TestAnErrorAfterTheClustersStatusAddsNoSecond(t *testing.T) {
	_, _, conn := execSession(t, "exec", "v4.channel.k8s.io", func(a *fakeAgent, id string) {
		_ = a.send(Message{Type: MessageStreamData, ID: id,
			StreamData: &StreamData{Data: append([]byte{channelError},
				`{"metadata":{},"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"2"}]}}`...), Binary: true}})
		_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{Error: "upstream reset"}})
	})
	frames, _ := readUntilClose(t, conn)
	statuses := statusFrames(frames)
	if len(statuses) != 1 || statuses[0]["reason"] != "NonZeroExitCode" {
		t.Fatalf("statuses = %v, want the cluster's own exit status alone", statuses)
	}
}

// Only exec and attach on v4/v5 read a Status there. port-forward's channels
// carry data, and the original channel.k8s.io carries a bare string.
func TestCarriesStatusChannel(t *testing.T) {
	for _, tc := range []struct {
		subresource, subprotocol string
		want                     bool
	}{
		{"exec", "v5.channel.k8s.io", true},
		{"exec", "v4.channel.k8s.io", true},
		{"attach", "v4.channel.k8s.io", true},
		{"exec", "channel.k8s.io", false},
		{"exec", "", false},
		{"portforward", "v2.portforward.k8s.io", false},
		{"portforward", "v4.channel.k8s.io", false},
	} {
		if got := carriesStatusChannel(APIPath{Subresource: tc.subresource}, tc.subprotocol); got != tc.want {
			t.Errorf("%s over %q = %v, want %v", tc.subresource, tc.subprotocol, got, tc.want)
		}
	}
}

// A port-forward the tunnel ended gets no Status frame — on its channels that
// would be bytes delivered to somebody's local port. Its close is then the only
// thing that says the session broke, so it is a 1011: the cause is read when
// the session ends, not when the close was deferred.
func TestATunnelDropAddsNoStatusToAPortForward(t *testing.T) {
	_, agent, conn := execSession(t, "portforward", "v2.portforward.k8s.io", nil)
	agent.close()
	frames, closeErr := readUntilClose(t, conn)
	if len(statusFrames(frames)) != 0 || len(frames) != 0 {
		t.Fatalf("a port-forward must get nothing on its channels, got %q", frames)
	}
	if closeErr.Code != websocket.CloseInternalServerErr {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseInternalServerErr)
	}
}

// kubectl's own executor, end to end against the gateway: the client-go code
// that decides kubectl's exit status, rather than a reading of the frames.
func TestKubectlExecutorReportsALostSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		then func(a *fakeAgent, id string)
		want string
	}{
		{"agent stopped cleanly", func(a *fakeAgent, id string) {
			_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
		}, errNoSessionStatus.Error()},
		{"agent named a cause", func(a *fakeAgent, id string) {
			_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{Error: "the kubemg agent stopped before the session ended"}})
		}, "the kubemg agent stopped before the session ended"},
		// The cause is the tunnel's own read error, whatever the socket said.
		{"tunnel dropped", func(a *fakeAgent, _ string) { a.close() }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runKubectlExec(t, tc.then)
			if err == nil {
				t.Fatal("a lost session must not read as success")
			}
			if !strings.Contains(err.Error(), "kubemg lost the session before the command finished: "+tc.want) {
				t.Fatalf("err = %q, want the Failure Status's message", err)
			}
		})
	}

	t.Run("cluster's own exit code", func(t *testing.T) {
		err := runKubectlExec(t, func(a *fakeAgent, id string) {
			_ = a.send(Message{Type: MessageStreamData, ID: id, StreamData: &StreamData{Binary: true, Data: append([]byte{channelError},
				`{"metadata":{},"status":"Failure","message":"command terminated with non-zero exit code","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"3"}]}}`...)}})
			_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
		})
		var exit exec.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 3 {
			t.Fatalf("err = %v, want the cluster's exit code 3 unchanged", err)
		}
	})

	t.Run("cluster's success", func(t *testing.T) {
		if err := runKubectlExec(t, func(a *fakeAgent, id string) {
			_ = a.send(Message{Type: MessageStreamData, ID: id, StreamData: &StreamData{Binary: true,
				Data: append([]byte{channelError}, `{"metadata":{},"status":"Success"}`...)}})
			_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
		}); err != nil {
			t.Fatalf("a finished command must stay a success: %v", err)
		}
	})
}

func runKubectlExec(t *testing.T, then func(a *fakeAgent, id string)) error {
	t.Helper()
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)
	session := func(a *fakeAgent, id string, open StreamOpen) {
		_ = a.send(Message{Type: MessageStreamStart, ID: id, StreamStart: &StreamStart{
			Status: http.StatusSwitchingProtocols, Subprotocol: open.Subprotocols[0],
		}})
		then(a, id)
	}
	if _, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), session); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	target, err := url.Parse(h.server.URL +
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/exec?command=sh&stdout=true&stderr=true")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := remotecommand.NewWebSocketExecutor(&rest.Config{
		Host: h.server.URL, BearerToken: h.token(admin),
	}, http.MethodGet, target.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard, Stderr: io.Discard})
}

// What the e2e pass found: an agent that is stopping closes the session itself,
// cleanly, so the stream carries no error. The cluster never said how the
// command finished, and that alone makes it a failure — older agents included.
func TestACleanCloseWithNoStatusIsAFailure(t *testing.T) {
	h, _, conn := execSession(t, "exec", "v5.channel.k8s.io", func(a *fakeAgent, id string) {
		_ = a.send(Message{Type: MessageStreamData, ID: id,
			StreamData: &StreamData{Data: []byte{1, '$', ' '}, Binary: true}})
		_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
	})
	frames, closeErr := readUntilClose(t, conn)
	statuses := statusFrames(frames)
	if len(statuses) != 1 || statuses[0]["status"] != "Failure" ||
		!strings.Contains(statuses[0]["message"].(string), errNoSessionStatus.Error()) {
		t.Fatalf("statuses = %v, want one Failure naming the missing status", statuses)
	}
	if closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseNormalClosure)
	}
	waitFor(t, func() bool {
		for _, event := range h.audit.events() {
			if event.Phase == PhaseClose && event.Error == errNoSessionStatus.Error() {
				return true
			}
		}
		return false
	})
}
