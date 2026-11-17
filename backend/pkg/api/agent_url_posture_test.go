package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * #360: with agents given an address of their own, the browser shell's kubectl
 * still dials the public URL from inside the cluster, and the agent URL was set
 * only in the environment, where an operator could not read it back.
 */

func agentPosture(t *testing.T, agentURL string, deployment Deployment) deploymentPostureResponse {
	t.Helper()
	env := newTestEnvWith(t, func(o *Options) {
		o.AgentURL = agentURL
		o.Deployment = deployment
	})
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	return decode[deploymentPostureResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings/deployment", env.tokenFor(t, admin), nil))
}

func TestDeploymentShowsTheAgentURL(t *testing.T) {
	body := agentPosture(t, "https://agents.example.com",
		Deployment{TLSEnabled: true, AgentListenAddr: ":8444"})
	check, ok := checkByKey(body.Checks, "agent-url")
	if !ok || check.Severity != checkOK {
		t.Fatalf("expected the agent URL listed, got %v", body.Checks)
	}
	if !strings.Contains(check.Title, "https://agents.example.com") {
		t.Fatalf("the title must name the address install packages carry, got %q", check.Title)
	}
	for _, want := range []string{":8444", "browser shell", "public URL"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("the detail does not mention %q: %q", want, check.Detail)
		}
	}
}

func TestDeploymentWarnsOfAPlainHTTPAgentURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  bool
		says string
	}{
		{"on a TLS listener", true, "no handshake"},
		{"without TLS", false, "in the clear"},
	} {
		body := agentPosture(t, "http://agents.example.com:8444", Deployment{TLSEnabled: tc.tls})
		check, ok := checkByKey(body.Checks, "agent-url")
		if !ok || check.Severity != checkWarn {
			t.Fatalf("%s: expected a warning, got %v", tc.name, body.Checks)
		}
		if !strings.Contains(check.Detail, tc.says) {
			t.Errorf("%s: detail %q does not say %q", tc.name, check.Detail, tc.says)
		}
		if check.Fix != "KUBEMG_AGENT_URL=https://agents.example.com:8444" {
			t.Errorf("%s: fix = %q", tc.name, check.Fix)
		}
	}
}

func TestDeploymentSaysNothingOfAnAgentURLThatIsNotSet(t *testing.T) {
	body := agentPosture(t, "", Deployment{TLSEnabled: true})
	if check, ok := checkByKey(body.Checks, "agent-url"); ok {
		t.Fatalf("no agent URL is set, yet the posture lists %v", check)
	}
}

func TestShellSaysWhereItsKubectlDialsWhenAgentsHaveTheirOwnAddress(t *testing.T) {
	for _, tc := range []struct {
		agentURL string
		want     string
	}{
		{"https://agents.example.com", "https://kubemg.example.com"},
		{"", ""},
		// The same address as the public URL is no separate address at all.
		{"https://kubemg.example.com", ""},
	} {
		env := newTestEnvWith(t, func(opts *Options) {
			opts.ShellEnabled = true
			opts.ShellImage = "ghcr.io/kubemg/kubemg-shell:test"
			opts.AgentURL = tc.agentURL
		})
		user := env.store.addUser("ada", "pw", db.RoleUser)
		cluster := env.store.addAgentCluster("edge", db.EnvProd, "token")
		env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

		rec := env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("agent URL %q: status %d (%s)", tc.agentURL, rec.Code, rec.Body.String())
		}
		if got := decode[shellResponse](t, rec).KubectlURL; got != tc.want {
			t.Errorf("agent URL %q: kubectl_url = %q, want %q", tc.agentURL, got, tc.want)
		}
	}
}
