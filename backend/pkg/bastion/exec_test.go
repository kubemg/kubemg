package bastion

import (
	"context"
	"testing"
)

// recorderFunc adapts a function into a SessionRecorder.
type recorderFunc func(SessionMeta) SessionSink

func (f recorderFunc) Begin(_ context.Context, meta SessionMeta) SessionSink { return f(meta) }

// recordingSink collects what a session would have written to disk.
type recordingSink struct {
	output  []string
	input   []string
	resizes [][2]int
	closed  bool
}

func (s *recordingSink) Output(data []byte)     { s.output = append(s.output, string(data)) }
func (s *recordingSink) Input(data []byte)      { s.input = append(s.input, string(data)) }
func (s *recordingSink) Resize(cols, rows int)  { s.resizes = append(s.resizes, [2]int{cols, rows}) }
func (s *recordingSink) Close(error)            { s.closed = true }

// frame builds a channel-protocol frame the way both ends of an exec do.
func frame(channel byte, payload string) []byte {
	return append([]byte{channel}, payload...)
}

func TestRecordingReadsTheChannelPrefix(t *testing.T) {
	sink := &recordingSink{}

	recordFromCluster(sink, frame(channelStdout, "hello"))
	recordFromCluster(sink, frame(channelStderr, "oops"))
	// The error channel is the API server talking *about* the session, not the
	// session, so it must not land in the recording.
	recordFromCluster(sink, frame(channelError, `{"status":"Success"}`))

	if len(sink.output) != 2 || sink.output[0] != "hello" || sink.output[1] != "oops" {
		t.Fatalf("stdout and stderr are one stream in a replay, got %v", sink.output)
	}

	recordFromClient(sink, frame(channelStdin, "id\r"))
	recordFromClient(sink, frame(channelResize, `{"Width":120,"Height":40}`))
	if len(sink.input) != 1 || sink.input[0] != "id\r" {
		t.Fatalf("keystrokes did not land, got %v", sink.input)
	}
	if len(sink.resizes) != 1 || sink.resizes[0] != [2]int{120, 40} {
		t.Fatalf("the window geometry did not land, got %v", sink.resizes)
	}
}

func TestRecordingIgnoresEmptyAndNilFrames(t *testing.T) {
	sink := &recordingSink{}

	// A bare channel byte carries nothing; a nil sink means recording is off.
	recordFromCluster(sink, []byte{channelStdout})
	recordFromClient(sink, []byte{})
	recordFromCluster(nil, frame(channelStdout, "hello"))
	recordFromClient(nil, frame(channelStdin, "id"))

	if len(sink.output) != 0 || len(sink.input) != 0 {
		t.Fatalf("nothing should have been recorded, got %v / %v", sink.output, sink.input)
	}
}

func TestBeginRecordingOnlyRecordsTerminalSessions(t *testing.T) {
	sink := &recordingSink{}
	proxy := &Proxy{recorder: recorderFunc(func(SessionMeta) SessionSink { return sink })}

	cases := map[string]bool{
		"/api/v1/namespaces/team-a/pods/api-0/exec?container=api&command=%2Fbin%2Fsh":   true,
		"/api/v1/namespaces/team-a/pods/api-0/attach?container=api":                     true,
		"/api/v1/namespaces/team-a/pods/api-0/portforward?ports=8080":                   false,
		"/api/v1/namespaces/team-a/pods/api-0/log?follow=true":                          false,
	}
	for path, wanted := range cases {
		event := &Event{SessionID: "abc", Path: path}
		got := proxy.beginRecording(t.Context(), event, ParsePath(path)) != nil
		if got != wanted {
			t.Fatalf("%s: recorded=%v, wanted %v", path, got, wanted)
		}
	}

	// A session with no id cannot be correlated with its audit records, so it is
	// not recorded either.
	path := "/api/v1/namespaces/team-a/pods/api-0/exec?container=api"
	if proxy.beginRecording(t.Context(), &Event{Path: path}, ParsePath(path)) != nil {
		t.Fatal("a session with no id must not be recorded")
	}
}

func TestBeginRecordingDescribesTheSessionFromTheRequest(t *testing.T) {
	var captured SessionMeta
	proxy := &Proxy{recorder: recorderFunc(func(meta SessionMeta) SessionSink {
		captured = meta
		return &recordingSink{}
	})}

	path := "/api/v1/namespaces/team-a/pods/api-0/exec" +
		"?container=api&command=%2Fbin%2Fsh&command=-c&command=id&tty=true&stdin=true"
	event := &Event{
		SessionID: "abc",
		UserID:    7,
		Username:  "devops",
		ClusterID: 3,
		Cluster:   "edge-us",
		Path:      path,
	}
	if proxy.beginRecording(t.Context(), event, ParsePath(path)) == nil {
		t.Fatal("expected a recording to start")
	}

	if captured.Namespace != "team-a" || captured.Pod != "api-0" || captured.Container != "api" {
		t.Fatalf("the recording is filed under the wrong object: %+v", captured)
	}
	if captured.Shell != "/bin/sh -c id" {
		t.Fatalf("expected the full argv, got %q", captured.Shell)
	}
	if captured.Verb != "exec" || !captured.TTY || !captured.HasStdin {
		t.Fatalf("the session parameters did not survive: %+v", captured)
	}
	if captured.Username != "devops" || captured.ClusterID != 3 {
		t.Fatalf("the identity did not survive: %+v", captured)
	}
}

func TestNewSessionIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := newSessionID()
		if len(id) != 32 {
			t.Fatalf("expected a 128 bit hex id, got %q", id)
		}
		if seen[id] {
			t.Fatalf("session id %q was minted twice", id)
		}
		seen[id] = true
	}
}
