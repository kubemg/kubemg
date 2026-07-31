package bastion

import (
	"bytes"

	"github.com/kubemg/kubemg/backend/pkg/guardrails"
)

/*
 * Watching what is typed into a shell.
 *
 * An interactive exec is one API call carrying an entire session. The API server
 * sees a connection open and, some minutes later, close; every destructive thing
 * done inside it is invisible to the cluster's own audit and unreachable by its
 * RBAC. The bastion is the only place those keystrokes exist as text, because it
 * is already carrying them for the recording.
 *
 * So this is a line editor. A terminal sends one keystroke per frame, and the
 * command is only a command once Enter is pressed — which means the guard has to
 * accumulate, honour the editing keys, and evaluate at the newline. Getting the
 * editing keys wrong does not merely mis-detect: a buffer that ignores backspace
 * would evaluate "rm -rf /tmpX<BS>" as though the X were still there.
 *
 * The refusal has two halves, and the second is the one that is easy to miss.
 * Suppressing the Enter keeps the command from running, but every character of it
 * is already sitting in the remote shell's line buffer — they were forwarded as
 * they were typed. Left there, the next Enter runs it, and the operator's next
 * command is appended to a line that starts with `rm -rf /`. So the guard also
 * sends Ctrl-U, which is what actually clears it.
 */

// Control bytes the line buffer honours. These are the editing keys a terminal
// sends in-band; everything else is either printable or irrelevant to what the
// shell will eventually execute.
const (
	keyCtrlC     = 0x03
	keyBackspace = 0x08
	keyCtrlU     = 0x15
	keyEscape    = 0x1b
	keyDelete    = 0x7f
)

// maxCommandBuffer bounds one accumulated line. A paste can be arbitrarily long
// and a line nobody ever terminates would otherwise grow without limit; at the
// cap the buffer is evaluated and reset, so an overlong line is still checked
// rather than silently abandoned.
const maxCommandBuffer = 8192

// commandGuard accumulates one session's keystrokes and evaluates each completed
// line. A session with no engine, or one that is not a terminal, gets a nil
// guard and the bridge stays exactly as it was.
type commandGuard struct {
	engine    *guardrails.Engine
	clusterID uint
	// tty is whether the session has a pty. It decides only how a refusal is
	// delivered: with a line editor on the far side there is a line to clear.
	tty bool
	buf []byte
}

// newCommandGuard returns a guard for an interactive session, or nil when there
// is nothing to guard.
func newCommandGuard(engine *guardrails.Engine, clusterID uint, parsed APIPath, path string) *commandGuard {
	if engine == nil {
		return nil
	}
	// Only a shell. A port-forward carries arbitrary TCP, where a byte that looks
	// like a newline is not the end of a command and the whole idea of a "line" is
	// a category error.
	switch parsed.Subresource {
	case "exec", "attach":
	default:
		return nil
	}
	// Nothing is typed into a session with no stdin, and its command was already
	// checked at open.
	query := queryOf(path)
	if query.Get("stdin") != "true" {
		return nil
	}
	return &commandGuard{
		engine:    engine,
		clusterID: clusterID,
		tty:       query.Get("tty") == "true",
	}
}

// inspect examines one frame on its way from the client to the cluster.
//
// It returns the decision, if any, and whether the frame should still be
// forwarded. A blocked frame is dropped whole: it is the Enter that would have
// run the command, and there is nothing in it worth passing on.
func (g *commandGuard) inspect(frame []byte) (*guardrails.Decision, bool) {
	if g == nil || len(frame) < 2 || frame[0] != channelStdin {
		return nil, true
	}

	var blocked *guardrails.Decision
	var warned *guardrails.Decision

	for _, b := range frame[1:] {
		switch b {
		case '\r', '\n':
			// A completed line. Both terminators are checked rather than only
			// one: a pty sends CR and a piped stdin sends LF, and a guard that
			// knew about one shape would be avoidable by using the other.
			if decision := g.evaluate(); decision != nil {
				if decision.Blocked() {
					blocked = decision
				} else if warned == nil {
					warned = decision
				}
			}
			g.buf = g.buf[:0]
		case keyCtrlC, keyCtrlU:
			// The line was abandoned. Nothing was run, so nothing is evaluated.
			g.buf = g.buf[:0]
		case keyBackspace, keyDelete:
			if len(g.buf) > 0 {
				g.buf = g.buf[:len(g.buf)-1]
			}
		case keyEscape:
			// An escape sequence — an arrow key, a history recall. What the line
			// becomes afterwards cannot be reconstructed from the bytes alone, so
			// the buffer is dropped rather than left holding a line that no longer
			// matches what the operator sees. Erring toward not matching is the
			// right side here: see the note on evasion below.
			g.buf = g.buf[:0]
		default:
			if len(g.buf) >= maxCommandBuffer {
				if decision := g.evaluate(); decision != nil && decision.Blocked() {
					blocked = decision
				}
				g.buf = g.buf[:0]
			}
			g.buf = append(g.buf, b)
		}
	}

	if blocked != nil {
		return blocked, false
	}
	return warned, true
}

// evaluate matches the accumulated line.
func (g *commandGuard) evaluate() *guardrails.Decision {
	line := bytes.TrimSpace(g.buf)
	if len(line) == 0 {
		return nil
	}
	return g.engine.EvaluateTerminalInput(g.clusterID, string(line))
}

/*
 * On evasion.
 *
 * A keystroke guard is not a sandbox and must not be sold as one. Anyone who can
 * open a shell can defeat a pattern with a variable, a base64 pipe or an editor,
 * and no amount of matching changes that — the protection against a determined
 * insider is the grant that let them in, plus the recording of what they did.
 *
 * What this does stop is the actual failure mode: the right command typed against
 * the wrong cluster, by someone who fully intended to run it somewhere else. That
 * person is not evading anything, and for them a rule that fires on the obvious
 * spelling is the difference between a near miss and an outage.
 */

// hasTTY reports whether the far side has a line editor to clear. It tolerates a
// nil guard so the bridge never has to ask whether guarding is on.
func (g *commandGuard) hasTTY() bool { return g != nil && g.tty }

// clearLineFrame is the stdin frame that empties the remote shell's line buffer.
// Without it, suppressing the Enter leaves the whole command sitting on the far
// side, one keypress from running.
func clearLineFrame() []byte { return []byte{channelStdin, keyCtrlU} }

// guardNoticeFrame renders a refusal for the operator's screen.
//
// It goes out on stderr rather than stdout because that is what it is — the
// gateway talking about the session, not the container talking in it — and
// because a terminal shows both. The CRLF and the leading newline matter on a
// pty: the cursor is sitting after the command that was just refused, and
// without them the message lands on top of it.
func guardNoticeFrame(decision *guardrails.Decision) []byte {
	message := "\r\n\x1b[1;31m" + decision.Message() + "\x1b[0m\r\n"
	return append([]byte{channelStderr}, message...)
}
