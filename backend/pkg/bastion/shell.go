package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Two execs the gateway makes on its own behalf.
 *
 * Everything else in this package carries a call somebody's kubectl or browser
 * made. These two carry one KubeMG makes: the browser shell is a pod KubeMG
 * created in the target cluster, and neither writing that pod's kubeconfig into
 * it nor attaching a terminal to it is an act the caller's own grant can
 * authorize — the pod lives in the agent's namespace, and a read-only grant
 * cannot exec anywhere at all.
 *
 * So the identity asserted to the API server is KubeMG's (shell.RunnerUser,
 * bound to a Role in that one namespace), while the identity in the audit trail
 * is the **person**. That split is the whole point and it is stated on the
 * record itself: Username is who did it, ImpersonatedUser is who the cluster
 * saw. A trail that named the runner would say a service opened a shell; a
 * cluster that saw the person would refuse the exec. Both halves are true at
 * once and the record carries both.
 *
 * Nothing else changes. The session is recorded like any other exec, the
 * keystroke guardrails inspect it like any other terminal, and both of its audit
 * records are written by the same code the console's pod terminal goes through.
 */

// v4Channel is the exec channel protocol used for KubeMG's own execs. v4 rather
// than v5 deliberately: v5 exists to signal a half-closed stdin, and the one
// place that would matter is avoided by shell.SeedCommand reading an exact byte
// count instead of waiting for EOF. v4 is what every supported API server
// speaks.
const v4Channel = "v4.channel.k8s.io"

// execChunk bounds one stdin frame. The agent caps a frame well above this; the
// number matters only in that a kubeconfig is written in two or three writes
// rather than one enormous one.
const execChunk = 16 << 10

// execTimeout bounds a non-interactive exec. Writing a file into a pod that is
// already running takes milliseconds; anything approaching this is a stream that
// is not going to finish.
const execTimeout = 30 * time.Second

// ShellSpec describes a terminal KubeMG is opening into a pod of its own.
type ShellSpec struct {
	// User is the person, for the audit trail and the recording.
	User *db.User
	// Cluster is where the pod is.
	Cluster *db.Cluster
	// Impersonate and Groups are the identity the API server is shown. These are
	// KubeMG's own — see the note at the top of this file.
	Impersonate string
	Groups      []string

	Namespace string
	Pod       string
	Container string
	// Command is the argv to run. It is built by the caller and never taken from
	// the wire: the browser asks for "a shell", not for a command line.
	Command []string

	// Activity is called for every frame the operator types, so the lifecycle can
	// keep an idle clock without this package knowing what one is. It must not
	// block — it runs on the socket's read loop.
	Activity func()
}

// ServeShell attaches the caller's browser to a pod KubeMG runs.
func (p *Proxy) ServeShell(c *gin.Context, spec ShellSpec) {
	started := time.Now()

	path := execPath(spec.Namespace, spec.Pod, spec.Container, spec.Command, true)
	event := Event{
		At:        started.UTC(),
		UserID:    spec.User.ID,
		Username:  spec.User.Username,
		ClusterID: spec.Cluster.ID,
		Cluster:   spec.Cluster.Name,
		Verb:      "exec",
		Method:    http.MethodGet,
		Path:      path,
		Namespace: spec.Namespace,
		Resource:  "pods",
		// The cluster sees KubeMG, not the operator. Recorded as such.
		ImpersonatedUser:   spec.Impersonate,
		ImpersonatedGroups: spec.Groups,
		Streaming:          true,
		SessionID:          newSessionID(),
	}

	tunnel, ok := p.registry.Get(spec.Cluster.ID)
	if !ok {
		p.fail(c, &event, http.StatusServiceUnavailable, ErrNoTunnel.Error())
		return
	}

	header := map[string][]string{
		"Impersonate-User":  {spec.Impersonate},
		"Impersonate-Group": spec.Groups,
	}
	p.serveUpgradeStream(c, tunnel, &event, header, []string{v4Channel}, ParsePath(path), spec.Activity)
}

// ExecSpec is one non-interactive command in a pod, with an optional payload on
// its standard input.
type ExecSpec struct {
	User      *db.User
	Cluster   *db.Cluster
	Impersonate string
	Groups      []string

	Namespace string
	Pod       string
	Container string
	Command   []string
	// Stdin is written to the process. It never appears in the command line, in
	// the pod's spec or in the audit trail — which is exactly why the shell's
	// credential travels this way.
	Stdin []byte
}

// ExecResult is what the command said and how it ended.
type ExecResult struct {
	Stdout []byte
	Stderr []byte
	// Status is the API server's own account of the exit, from the error channel.
	// An empty status is a command that ended without one being sent, which the
	// API server does for a clean exit on some versions.
	Status string
	// Failed reports a non-zero exit.
	Failed bool
}

// ExecOnce runs a command in a pod and returns its output.
//
// It is the request/response shape of an exec: open the stream, write stdin,
// read until the far side is done. Nothing about it is interactive, so there is
// no terminal, no recording — a recording of a file being written is a recording
// of a credential — and no keystroke guard, because there are no keystrokes.
func (p *Proxy) ExecOnce(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	started := time.Now()
	path := execPath(spec.Namespace, spec.Pod, spec.Container, spec.Command, false)

	event := Event{
		At:                 started.UTC(),
		UserID:             spec.User.ID,
		Username:           spec.User.Username,
		ClusterID:          spec.Cluster.ID,
		Cluster:            spec.Cluster.Name,
		Verb:               "exec",
		Method:             http.MethodGet,
		Path:               path,
		Namespace:          spec.Namespace,
		Resource:           "pods",
		ImpersonatedUser:   spec.Impersonate,
		ImpersonatedGroups: spec.Groups,
		Streaming:          true,
	}

	tunnel, ok := p.registry.Get(spec.Cluster.ID)
	if !ok {
		event.Status = http.StatusServiceUnavailable
		event.Error = ErrNoTunnel.Error()
		event.Duration = time.Since(started)
		p.auditor.Record(ctx, event)
		return nil, ErrNoTunnel
	}

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	stream, head, err := tunnel.OpenStream(ctx, &StreamOpen{
		Method: http.MethodGet,
		Path:   path,
		Header: map[string][]string{
			"Impersonate-User":  {spec.Impersonate},
			"Impersonate-Group": spec.Groups,
		},
		Upgrade:      true,
		Subprotocols: []string{v4Channel},
	})
	if err != nil {
		event.Status, _ = tunnelFailure(err)
		event.Error = err.Error()
		event.Duration = time.Since(started)
		p.auditor.Record(ctx, event)
		return nil, err
	}
	defer stream.Close(nil)

	if head.Error != "" {
		event.Status = http.StatusBadGateway
		event.Error = head.Error
		event.Duration = time.Since(started)
		p.auditor.Record(ctx, event)
		return nil, fmt.Errorf("exec did not open: %s", head.Error)
	}

	// The open half of the trail, written before anything is sent — the rule
	// every streaming call here follows.
	open := event
	open.Phase = PhaseOpen
	open.Status = http.StatusSwitchingProtocols
	open.Duration = time.Since(started)
	p.auditor.Record(ctx, open)

	result, execErr := pumpExec(ctx, stream, spec.Stdin)

	closing := event
	closing.Phase = PhaseClose
	closing.Status = http.StatusSwitchingProtocols
	closing.Duration = time.Since(started)
	closing.BytesIn = int64(len(spec.Stdin))
	if result != nil {
		closing.BytesOut = int64(len(result.Stdout) + len(result.Stderr))
	}
	if execErr != nil {
		closing.Error = execErr.Error()
	} else if result != nil && result.Failed {
		closing.Error = result.Status
	}
	p.auditor.Record(ctx, closing)

	return result, execErr
}

// pumpExec writes stdin and reads the three channels the API server answers on.
func pumpExec(ctx context.Context, stream *Stream, stdin []byte) (*ExecResult, error) {
	for offset := 0; offset < len(stdin); offset += execChunk {
		end := min(offset+execChunk, len(stdin))
		frame := make([]byte, 0, end-offset+1)
		frame = append(frame, channelStdin)
		frame = append(frame, stdin[offset:end]...)
		if err := stream.Send(StreamData{Data: frame, Binary: true}); err != nil {
			return nil, err
		}
	}

	result := &ExecResult{}
	for {
		select {
		case chunk, open := <-stream.Chunks():
			if !open {
				return result, stream.Err()
			}
			stream.Consumed(len(chunk.Data))
			if len(chunk.Data) < 1 {
				continue
			}
			switch chunk.Data[0] {
			case channelStdout:
				result.Stdout = append(result.Stdout, chunk.Data[1:]...)
			case channelStderr:
				result.Stderr = append(result.Stderr, chunk.Data[1:]...)
			case channelError:
				// The error channel is the API server's own verdict on the exit,
				// and it arrives last. Reading it is what lets this return "the
				// command failed" rather than "the stream ended".
				result.Status, result.Failed = readExecStatus(chunk.Data[1:])
				return result, nil
			}
		case <-stream.Done():
			return result, stream.Err()
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// readExecStatus decodes the metav1.Status the error channel carries.
func readExecStatus(payload []byte) (string, bool) {
	var status struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &status); err != nil {
		return strings.TrimSpace(string(payload)), true
	}
	if status.Status == "Success" {
		return "", false
	}
	message := status.Message
	if message == "" {
		message = status.Reason
	}
	if message == "" {
		message = "the command failed"
	}
	return message, true
}

// execPath renders the exec subresource URL. Every parameter is built here
// rather than forwarded, so nothing a browser sends can change what runs.
func execPath(namespace, pod, container string, command []string, tty bool) string {
	query := url.Values{}
	query.Set("container", container)
	query.Set("stdin", "true")
	query.Set("stdout", "true")
	query.Set("stderr", "true")
	query.Set("tty", boolString(tty))
	for _, argument := range command {
		query.Add("command", argument)
	}
	return fmt.Sprintf(
		"/api/v1/namespaces/%s/pods/%s/exec?%s",
		url.PathEscape(namespace), url.PathEscape(pod), query.Encode(),
	)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// ErrExecFailed reports a command that ran and returned non-zero. Callers that
// only need "did it work" compare against this rather than parsing a status.
var ErrExecFailed = errors.New("command failed inside the pod")
