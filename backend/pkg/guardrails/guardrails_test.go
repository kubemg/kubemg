package guardrails

import (
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// policy is a terse constructor for the table-driven tests below.
func policy(name string, clusterID uint, pattern, target, action string) db.GuardrailPolicy {
	return db.GuardrailPolicy{
		ID:        1,
		Name:      name,
		ClusterID: clusterID,
		Pattern:   pattern,
		Target:    target,
		Action:    action,
		Enabled:   true,
	}
}

func engineWith(t *testing.T, policies ...db.GuardrailPolicy) *Engine {
	t.Helper()
	snapshot, problems := Compile(policies)
	for _, problem := range problems {
		t.Fatalf("unexpected compile problem: %v", problem)
	}
	engine := New()
	engine.Store(snapshot)
	return engine
}

// The nil engine is the state a gateway wired without guardrails is in, and the
// state every install was in before they existed. It must refuse nothing rather
// than panic — this is the failure mode the whole package is designed around.
func TestNilEngineAllowsEverything(t *testing.T) {
	var engine *Engine

	if decision := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod"); decision != nil {
		t.Fatalf("a nil engine must refuse nothing, got %+v", decision)
	}
	if decision := engine.EvaluateTerminalInput(1, "rm -rf /"); decision != nil {
		t.Fatalf("a nil engine must refuse nothing, got %+v", decision)
	}
	if engine.Snapshot().Rules() != 0 {
		t.Fatal("a nil engine holds no rules")
	}
}

// An engine that has been created but never published to is the window between
// boot and the first refresh. It has to behave like the nil one.
func TestUnpublishedEngineAllowsEverything(t *testing.T) {
	engine := New()
	if decision := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod"); decision != nil {
		t.Fatalf("an unpublished engine must refuse nothing, got %+v", decision)
	}
}

func TestGlobalPolicyAppliesToEveryCluster(t *testing.T) {
	engine := engineWith(t, policy(
		"no namespace deletion", 0,
		`^DELETE /api/v1/namespaces/[^/?]+`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock,
	))

	for _, clusterID := range []uint{1, 2, 99} {
		decision := engine.EvaluateAPIRequest(clusterID, "DELETE", "/api/v1/namespaces/prod")
		if !decision.Blocked() {
			t.Fatalf("cluster %d: expected a block, got %+v", clusterID, decision)
		}
		if decision.Scope != ScopeGlobal {
			t.Fatalf("cluster %d: expected the global scope, got %q", clusterID, decision.Scope)
		}
	}
}

// The per-cluster half: production may be stricter than a sandbox, and a rule
// written for one cluster must not leak onto the next one.
func TestClusterPolicyDoesNotApplyElsewhere(t *testing.T) {
	engine := engineWith(t, policy(
		"cluster A only", 1,
		`^DELETE /api/v1/namespaces/default/pods/`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock,
	))

	path := "/api/v1/namespaces/default/pods/web-1"
	if !engine.EvaluateAPIRequest(1, "DELETE", path).Blocked() {
		t.Fatal("cluster 1 is covered by the rule and should be blocked")
	}
	if decision := engine.EvaluateAPIRequest(2, "DELETE", path); decision != nil {
		t.Fatalf("cluster 2 is not covered by the rule, got %+v", decision)
	}
}

func TestDisabledPolicyIsNotCompiled(t *testing.T) {
	disabled := policy("off", 0, `^DELETE /api/v1/namespaces/`,
		db.GuardrailTargetAPIRequest, db.GuardrailActionBlock)
	disabled.Enabled = false

	engine := engineWith(t, disabled)
	if engine.Snapshot().Rules() != 0 {
		t.Fatal("a disabled rule must not reach the hot path at all")
	}
	if decision := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod"); decision != nil {
		t.Fatalf("a disabled rule refuses nothing, got %+v", decision)
	}
}

// The subject is "METHOD /path", which is what makes the documented patterns
// read the way an operator wrote them. A rule about DELETE must not fire on GET.
func TestTheMethodIsPartOfTheSubject(t *testing.T) {
	engine := engineWith(t, policy(
		"no namespace deletion", 0,
		`^DELETE /api/v1/namespaces/[^/?]+`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock,
	))

	if !engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod").Blocked() {
		t.Fatal("expected the delete to be blocked")
	}
	if decision := engine.EvaluateAPIRequest(1, "GET", "/api/v1/namespaces/prod"); decision != nil {
		t.Fatalf("reading a namespace is not deleting one, got %+v", decision)
	}
}

func TestTargetSeparatesAPICallsFromCommands(t *testing.T) {
	engine := engineWith(t,
		policy("api only", 0, `dangerous`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock),
		policy("shell only", 0, `rm -rf /`, db.GuardrailTargetTerminalExec, db.GuardrailActionBlock),
	)

	if decision := engine.EvaluateTerminalInput(1, "echo dangerous"); decision != nil {
		t.Fatalf("an api_request rule must not match a typed command, got %+v", decision)
	}
	if decision := engine.EvaluateAPIRequest(1, "POST", "/rm -rf /"); decision != nil {
		t.Fatalf("a terminal_exec rule must not match an API call, got %+v", decision)
	}
	if !engine.EvaluateTerminalInput(1, "rm -rf /").Blocked() {
		t.Fatal("the shell rule should have matched the command")
	}
}

func TestBothTargetMatchesEitherSubject(t *testing.T) {
	engine := engineWith(t, policy("everywhere", 0, `prod-secret`,
		db.GuardrailTargetBoth, db.GuardrailActionBlock))

	if !engine.EvaluateTerminalInput(1, "cat prod-secret").Blocked() {
		t.Fatal("expected the command to be blocked")
	}
	if !engine.EvaluateAPIRequest(1, "GET", "/api/v1/namespaces/x/secrets/prod-secret").Blocked() {
		t.Fatal("expected the API call to be blocked")
	}
}

// A warn lets the call through but still produces a decision, which is what
// makes the trail usable for deciding whether to arm the rule.
func TestWarnDoesNotBlockButIsStillReported(t *testing.T) {
	engine := engineWith(t, policy("observe", 0, `^GET /api/v1/namespaces/[^/]+/secrets/`,
		db.GuardrailTargetAPIRequest, db.GuardrailActionWarn))

	decision := engine.EvaluateAPIRequest(1, "GET", "/api/v1/namespaces/prod/secrets/db")
	if decision == nil {
		t.Fatal("a warn rule still reports its match")
	}
	if decision.Blocked() {
		t.Fatal("a warn rule must not refuse the call")
	}
}

// A cluster rule that blocks must not be masked by a global rule that only
// warns — the question is whether the call is refused, and one rule saying so is
// enough however the others are ordered.
func TestABlockWinsOverAWarn(t *testing.T) {
	warn := policy("global warn", 0, `namespaces`, db.GuardrailTargetAPIRequest, db.GuardrailActionWarn)
	block := policy("cluster block", 1, `namespaces`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock)
	block.ID = 2

	engine := engineWith(t, warn, block)

	decision := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod")
	if !decision.Blocked() {
		t.Fatalf("the blocking rule must win, got %+v", decision)
	}
	if decision.Policy != "cluster block" {
		t.Fatalf("expected the blocking rule to be named, got %q", decision.Policy)
	}
}

// One bad regular expression must not take the rest of the rule set down with
// it: the alternative is that a typo in one rule silently disables every other.
func TestABadPatternIsSkippedNotFatal(t *testing.T) {
	good := policy("good", 0, `^DELETE /api/v1/nodes/`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock)
	bad := policy("bad", 0, `^DELETE /api/v1/(nodes`, db.GuardrailTargetAPIRequest, db.GuardrailActionBlock)
	bad.ID = 2

	snapshot, problems := Compile([]db.GuardrailPolicy{good, bad})
	if len(problems) != 1 {
		t.Fatalf("expected exactly one reported problem, got %d", len(problems))
	}
	if snapshot.Rules() != 1 {
		t.Fatalf("the good rule must survive, got %d rules", snapshot.Rules())
	}

	engine := New()
	engine.Store(snapshot)
	if !engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/nodes/node-1").Blocked() {
		t.Fatal("the surviving rule should still be enforced")
	}
}

// The catch that matters most: `.*` in a block rule is every request on every
// cluster refused, typed in two characters by someone who then cannot use the
// console to undo it.
func TestPatternsThatMatchEverythingAreRefused(t *testing.T) {
	for _, pattern := range []string{"", "   ", ".*", ".?", "^", "$", "(a)?", "^.*$"} {
		if err := ValidatePattern(pattern); err == nil {
			t.Fatalf("pattern %q matches every subject and must be refused", pattern)
		}
	}
}

func TestValidatePatternAcceptsRealRules(t *testing.T) {
	for _, template := range db.GuardrailTemplates {
		if err := ValidatePattern(template.Pattern); err != nil {
			t.Fatalf("preset %q does not validate: %v", template.Key, err)
		}
	}
}

func TestValidatePatternRejectsBrokenRegexp(t *testing.T) {
	if err := ValidatePattern(`^DELETE /api/(v1`); err == nil {
		t.Fatal("an uncompilable pattern must be refused at the form")
	}
}

// The presets are the rules most installs will actually run, so they are tested
// against the traffic they claim to catch rather than only for compiling.
func TestPresetsCatchWhatTheyClaim(t *testing.T) {
	byKey := func(t *testing.T, key string) *Engine {
		t.Helper()
		template, ok := db.GuardrailTemplateByKey(key)
		if !ok {
			t.Fatalf("no preset %q", key)
		}
		return engineWith(t, policy(template.Name, 0, template.Pattern, template.Target, template.Action))
	}

	t.Run("namespace deletion", func(t *testing.T) {
		engine := byKey(t, "delete-namespace")
		if !engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod").Blocked() {
			t.Fatal("expected `kubectl delete ns prod` to be blocked")
		}
		// Deleting something *inside* a namespace is an entirely ordinary act and
		// must not be caught by the rule about deleting the namespace itself.
		if d := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/prod/pods/web-1"); d != nil {
			t.Fatalf("deleting a pod is not deleting a namespace, got %+v", d)
		}
		if d := engine.EvaluateAPIRequest(1, "GET", "/api/v1/namespaces/prod"); d != nil {
			t.Fatalf("reading a namespace must be allowed, got %+v", d)
		}
	})

	t.Run("rm -rf", func(t *testing.T) {
		engine := byKey(t, "rm-rf-root")
		for _, command := range []string{
			"rm -rf /",
			"rm -rf /*",
			"rm -fr /",
			"rm -Rf /",
			"rm --recursive -rf /",
			"sudo rm -rf / --no-preserve-root",
		} {
			if !engine.EvaluateTerminalInput(1, command).Blocked() {
				t.Fatalf("expected %q to be blocked", command)
			}
		}
		// The false positives that would make an operator disable the rule.
		for _, command := range []string{
			"rm -rf /tmp/build",
			"rm -rf ./dist",
			"ls -l /",
			"rm file.txt",
		} {
			if decision := engine.EvaluateTerminalInput(1, command); decision != nil {
				t.Fatalf("%q is ordinary and must not be blocked, got %+v", command, decision)
			}
		}
	})

	// The collection rule is the one most at risk of over-matching: one trailing
	// path segment is the difference between "delete every pod here" and "delete
	// this pod".
	t.Run("bulk deletion", func(t *testing.T) {
		engine := byKey(t, "delete-collection")
		for _, path := range []string{
			"/api/v1/namespaces/default/pods",
			"/apis/apps/v1/namespaces/default/deployments?labelSelector=app%3Dweb",
		} {
			if !engine.EvaluateAPIRequest(1, "DELETE", path).Blocked() {
				t.Fatalf("expected the collection delete %q to be blocked", path)
			}
		}
		if d := engine.EvaluateAPIRequest(1, "DELETE", "/api/v1/namespaces/default/pods/web-1"); d != nil {
			t.Fatalf("deleting one named pod must be allowed, got %+v", d)
		}
	})

	t.Run("fork bomb", func(t *testing.T) {
		engine := byKey(t, "fork-bomb")
		if !engine.EvaluateTerminalInput(1, ":(){ :|:& };:").Blocked() {
			t.Fatal("expected the fork bomb to be blocked")
		}
	})

	t.Run("block device", func(t *testing.T) {
		engine := byKey(t, "disk-overwrite")
		if !engine.EvaluateTerminalInput(1, "dd if=/dev/zero of=/dev/sda bs=1M").Blocked() {
			t.Fatal("expected the disk overwrite to be blocked")
		}
		if !engine.EvaluateTerminalInput(1, "mkfs.ext4 /dev/nvme0n1").Blocked() {
			t.Fatal("expected the reformat to be blocked")
		}
		if d := engine.EvaluateTerminalInput(1, "dd if=/dev/urandom of=./seed bs=1M count=1"); d != nil {
			t.Fatalf("writing to a file is not writing to a device, got %+v", d)
		}
	})
}

// A very long subject is truncated rather than matched in full. The rule still
// has to fire on what survives, or padding would be an evasion.
func TestAnOverlongSubjectIsStillMatched(t *testing.T) {
	engine := engineWith(t, policy("rm", 0, `rm -rf /`,
		db.GuardrailTargetTerminalExec, db.GuardrailActionBlock))

	padded := "rm -rf /" + string(make([]byte, maxSubjectLength*2))
	if !engine.EvaluateTerminalInput(1, padded).Blocked() {
		t.Fatal("the interesting part is at the front and must still match")
	}
}

func TestEmptyTerminalInputIsNotEvaluated(t *testing.T) {
	engine := engineWith(t, policy("anything", 0, `x`,
		db.GuardrailTargetTerminalExec, db.GuardrailActionBlock))

	if decision := engine.EvaluateTerminalInput(1, "   \t "); decision != nil {
		t.Fatalf("an empty line is not a command, got %+v", decision)
	}
}
