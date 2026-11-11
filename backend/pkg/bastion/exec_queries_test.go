package bastion

import (
	"slices"
	"testing"
)

// A malformed pair does not hide the rest of the query. The API server's
// net/http drops the bad pair and reads the others, so an exec carrying one
// still runs its command; the guardrail has to see that command too.
func TestQueryOfKeepsWhatTheAPIServerReads(t *testing.T) {
	const path = "/api/v1/namespaces/team-a/pods/web-0/exec?bad=%zz&command=rm&command=-rf&command=%2F&stdin=true"
	query := queryOf(path)
	if got := query["command"]; !slices.Equal(got, []string{"rm", "-rf", "/"}) {
		t.Fatalf("command = %q, want the argv the cluster runs", got)
	}
	if query.Get("stdin") != "true" {
		t.Fatalf("stdin lost behind the malformed pair: %v", query)
	}
	if got := execCommand(path); got != "rm -rf /" {
		t.Fatalf("execCommand = %q, want the command line the guardrails match", got)
	}
	// A semicolon makes a pair invalid the same way.
	if got := execCommand("/api/v1/namespaces/a/pods/b/exec?x=1;y=2&command=reboot"); got != "reboot" {
		t.Fatalf("execCommand with a ';' pair = %q", got)
	}
}
