package db

import (
	"strings"
	"testing"
)

func TestCheckNamespacesIsTheKubernetesNamespaceRule(t *testing.T) {
	for _, good := range []string{"a", "team-a", "kube-system", "0", strings.Repeat("a", 63)} {
		if err := CheckNamespaces([]string{good}); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"[a-z]+", "a.b", "-a", "a-", "A", "a b", `a"`, "a|b", strings.Repeat("a", 64)} {
		if err := CheckNamespaces([]string{"ok", bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// Blank entries are dropped by JoinNamespaces, not refused.
	if err := CheckNamespaces([]string{"", "  "}); err != nil {
		t.Fatalf("blank entries refused: %v", err)
	}
}
