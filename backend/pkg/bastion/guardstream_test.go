package bastion

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
)

// guardEngineFor builds an engine holding one terminal rule.
func guardEngineFor(t *testing.T, pattern string) *guardrails.Engine {
	t.Helper()
	snapshot, problems := guardrails.Compile([]db.GuardrailPolicy{{
		ID:      1,
		Name:    "no rm -rf",
		Pattern: pattern,
		Target:  db.GuardrailTargetTerminalExec,
		Action:  db.GuardrailActionBlock,
		Enabled: true,
	}})
	if len(problems) != 0 {
		t.Fatalf("unexpected compile problems: %v", problems)
	}
	engine := guardrails.New()
	engine.Store(snapshot)
	return engine
}

// interactiveGuard is what an in-page terminal or `kubectl exec -it` produces.
func interactiveGuard(t *testing.T, engine *guardrails.Engine) *commandGuard {
	t.Helper()
	guard := newCommandGuard(engine, 1, APIPath{Subresource: "exec"},
		"/api/v1/namespaces/default/pods/web/exec?stdin=true&tty=true&command=bash")
	if guard == nil {
		t.Fatal("an interactive exec must be guarded")
	}
	return guard
}

// typeInto feeds a string to the guard one keystroke per frame, the way a
// terminal actually sends it. Anything else would test a shape that never
// occurs.
func typeInto(guard *commandGuard, text string) (*guardrails.Decision, bool) {
	var last *guardrails.Decision
	forwarded := true
	for _, b := range []byte(text) {
		decision, forward := guard.inspect([]byte{channelStdin, b})
		if decision != nil {
			last = decision
		}
		if !forward {
			forwarded = false
		}
	}
	return last, forwarded
}

func TestKeystrokesAreEvaluatedAtTheNewline(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	// Nothing is a command until Enter. Every keystroke before it must be
	// forwarded, or the operator's shell would stop echoing halfway through the
	// word and the session would look broken.
	for _, b := range []byte("rm -rf /") {
		if decision, forward := guard.inspect([]byte{channelStdin, b}); decision != nil || !forward {
			t.Fatalf("keystroke %q must pass through untouched", string(b))
		}
	}

	decision, forward := guard.inspect([]byte{channelStdin, '\r'})
	if !decision.Blocked() {
		t.Fatalf("the completed line should have been blocked, got %+v", decision)
	}
	if forward {
		t.Fatal("the Enter that would run the command must not be forwarded")
	}
}

// A line-feed terminator is what a piped stdin sends. Honouring only CR would
// leave the rule avoidable by not using a pty.
func TestLineFeedAlsoCompletesALine(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))
	decision, forward := typeInto(guard, "rm -rf /\n")
	if !decision.Blocked() || forward {
		t.Fatalf("a newline-terminated line must be evaluated, got %+v", decision)
	}
}

// The whole reason the guard is a line editor rather than a substring search: a
// buffer that ignored backspace would block a command the operator corrected.
func TestBackspaceRewritesTheLine(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	// "rm -rf /x", corrected back to "rm -rf /" — still dangerous, still blocked.
	if decision, _ := typeInto(guard, "rm -rf /x\x7f\r"); !decision.Blocked() {
		t.Fatalf("the corrected line is still `rm -rf /` and must be blocked, got %+v", decision)
	}

	// And the other direction: a line that only *looks* dangerous mid-typing.
	guard = interactiveGuard(t, guardEngineFor(t, `rm -rf /`))
	if decision, forward := typeInto(guard, "rm -rf /\x7ftmp/cache\r"); decision != nil || !forward {
		t.Fatalf("`rm -rf tmp/cache` is ordinary and must pass, got %+v", decision)
	}
}

func TestCtrlCAbandonsTheLine(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	// Typed, thought better of, cancelled. Nothing ran, so nothing is refused.
	if decision, forward := typeInto(guard, "rm -rf /\x03"); decision != nil || !forward {
		t.Fatalf("an abandoned line is not a command, got %+v", decision)
	}
	// And the buffer really is empty rather than merely unevaluated.
	if decision, _ := typeInto(guard, "ls\r"); decision != nil {
		t.Fatalf("the abandoned text must not survive into the next line, got %+v", decision)
	}
}

func TestAPastedCommandInOneFrameIsBlocked(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	// A paste arrives as one frame with the newline inside it, which is the
	// shape that would slip past a guard only looking at single keystrokes.
	frame := append([]byte{channelStdin}, "rm -rf /\r"...)
	decision, forward := guard.inspect(frame)
	if !decision.Blocked() {
		t.Fatalf("a pasted command must be evaluated, got %+v", decision)
	}
	if forward {
		t.Fatal("the frame carrying the blocked command must be dropped")
	}
}

// Only stdin is a command. The resize channel carries JSON that could contain
// anything, and treating it as typing would produce refusals nobody can explain.
func TestOnlyTheStdinChannelIsInspected(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	frame := append([]byte{channelResize}, `{"Width":80,"Height":24,"note":"rm -rf /"}`+"\r"...)
	if decision, forward := guard.inspect(frame); decision != nil || !forward {
		t.Fatalf("a resize frame is not a command, got %+v", decision)
	}
}

// port-forward carries arbitrary TCP behind the same channel framing, where a
// byte that looks like a newline is not the end of anything.
func TestPortForwardIsNotGuarded(t *testing.T) {
	engine := guardEngineFor(t, `rm -rf /`)
	guard := newCommandGuard(engine, 1, APIPath{Subresource: "portforward"},
		"/api/v1/namespaces/default/pods/web/portforward?ports=8080")
	if guard != nil {
		t.Fatal("a port-forward must not be treated as a terminal")
	}
}

// Without an engine the bridge must behave exactly as it did before guardrails
// existed — no guard, no allocation, no inspection.
func TestNoEngineMeansNoGuard(t *testing.T) {
	if guard := newCommandGuard(nil, 1, APIPath{Subresource: "exec"}, "?stdin=true&tty=true"); guard != nil {
		t.Fatal("a gateway without an engine must not guard anything")
	}
	// And a nil guard tolerates the calls the bridge makes unconditionally.
	var guard *commandGuard
	if decision, forward := guard.inspect([]byte{channelStdin, 'x'}); decision != nil || !forward {
		t.Fatal("a nil guard forwards everything")
	}
	if guard.hasTTY() {
		t.Fatal("a nil guard has no tty")
	}
}

// A session with no stdin has nothing typed into it; its command was already
// checked at open, and buffering output would be pure overhead.
func TestASessionWithoutStdinIsNotGuarded(t *testing.T) {
	engine := guardEngineFor(t, `rm -rf /`)
	guard := newCommandGuard(engine, 1, APIPath{Subresource: "exec"},
		"/api/v1/namespaces/default/pods/web/exec?command=ls")
	if guard != nil {
		t.Fatal("a session with no stdin cannot be typed into")
	}
}

// The buffer must not grow without bound on input that never terminates.
func TestTheLineBufferIsBounded(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	frame := append([]byte{channelStdin}, strings.Repeat("a", maxCommandBuffer*3)...)
	guard.inspect(frame)
	if len(guard.buf) > maxCommandBuffer {
		t.Fatalf("the buffer grew to %d bytes, past the %d cap", len(guard.buf), maxCommandBuffer)
	}
}

// Suppressing the Enter is only half a refusal: every character is already in
// the remote shell's line buffer, one keypress from running.
func TestTheClearLineFrameTargetsStdin(t *testing.T) {
	frame := clearLineFrame()
	if len(frame) != 2 || frame[0] != channelStdin || frame[1] != keyCtrlU {
		t.Fatalf("expected a Ctrl-U on stdin, got %v", frame)
	}
}

// The notice is the gateway talking about the session, not the container talking
// in it, so it goes out on stderr — and it has to name the rule.
func TestTheGuardNoticeGoesOutOnStderr(t *testing.T) {
	decision := &guardrails.Decision{Policy: "no rm -rf", Action: db.GuardrailActionBlock}
	frame := guardNoticeFrame(decision)

	if frame[0] != channelStderr {
		t.Fatalf("expected channel %d, got %d", channelStderr, frame[0])
	}
	if !strings.Contains(string(frame), "no rm -rf") {
		t.Fatalf("the notice must name the policy: %q", string(frame))
	}
}

// The non-interactive shape: `kubectl exec pod -- rm -rf /` types nothing at
// all, so the keystroke guard never sees it. It is also the shape a script uses.
func TestExecCommandReadsTheArgvFromTheQuery(t *testing.T) {
	path := "/api/v1/namespaces/default/pods/web/exec?command=rm&command=-rf&command=%2F&stdin=false"
	if got := execCommand(path); got != "rm -rf /" {
		t.Fatalf("expected the argv joined, got %q", got)
	}
	if got := execCommand("/api/v1/namespaces/default/pods/web/exec?stdin=true"); got != "" {
		t.Fatalf("an attach carries no command, got %q", got)
	}
}

// A Turkish "ş" is two bytes, an emoji is four, and a terminal splits neither on
// a character boundary it does not know about. Nothing between the operator's
// keyboard and the container may look at a byte and decide it is a character:
// the guard accumulates bytes, the wire carries bytes, and only the terminal at
// each end reassembles them. These two tests pin that down, because the failure
// mode is silent — a dropped or replaced byte reaches the shell as a different
// command than the one that was typed.
func TestMultiByteInputIsForwardedVerbatim(t *testing.T) {
	guard := interactiveGuard(t, guardEngineFor(t, `rm -rf /`))

	const typed = "echo şğüöçİ € 🚀\n"
	var seen []byte
	for _, b := range []byte(typed) {
		frame := []byte{channelStdin, b}
		if _, forward := guard.inspect(frame); !forward {
			t.Fatalf("a benign line must be forwarded, stopped at %#x", b)
		}
		seen = append(seen, frame[1:]...)
	}

	if string(seen) != typed {
		t.Fatalf("input was altered on the way through:\n got %q\nwant %q", seen, typed)
	}
}

func TestMultiByteInputSurvivesTheWire(t *testing.T) {
	// Every byte value, so a frame carrying a half-formed UTF-8 sequence — which
	// is exactly what one keystroke of a two-byte character is — is covered too.
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	encoded, err := json.Marshal(Message{
		Type:       MessageStreamData,
		ID:         "s1-1",
		StreamData: &StreamData{Data: payload, Binary: true},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.StreamData == nil {
		t.Fatal("the chunk did not survive the envelope")
	}
	if !bytes.Equal(decoded.StreamData.Data, payload) {
		t.Fatalf("the wire altered the payload:\n got %#v\nwant %#v",
			decoded.StreamData.Data, payload)
	}
	if !decoded.StreamData.Binary {
		t.Fatal("a binary chunk replayed as text would be re-encoded by the far end")
	}
}
