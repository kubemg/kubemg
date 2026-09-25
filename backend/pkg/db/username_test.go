package db

import (
	"errors"
	"testing"
)

func TestCheckUsername(t *testing.T) {
	for _, name := range []string{"ada", "ada.lovelace@example.com", "ADA-01", "ada_l", "Ada Lovelace"} {
		if err := CheckUsername(name); err != nil {
			t.Errorf("CheckUsername(%q) = %v, want accepted", name, err)
		}
	}
	for _, name := range []string{
		"system:masters",
		"system:serviceaccount:kube-system:backup-operator",
		"system:node:worker-1",
		"kubemg:alarm-watcher",
		"kubemg:u:ada",
		"oidc:ada",
		"ada\n",
		"ada\x00",
		"ada\tlovelace",
	} {
		if err := CheckUsername(name); !errors.Is(err, ErrUnsafeUsername) {
			t.Errorf("CheckUsername(%q) = %v, want ErrUnsafeUsername", name, err)
		}
	}
}

// A federated account found by name is only the same person when the stable
// identifier agrees. Otherwise a renamed `preferred_username` would sign one
// person into another's account.
func TestExternalIDConflict(t *testing.T) {
	cases := []struct {
		name     string
		stored   string
		asserted string
		want     bool
	}{
		{"same subject", "sub-ada", "sub-ada", false},
		{"different subject, same username", "sub-ada", "sub-mallory", true},
		{"account never recorded one", "", "sub-mallory", false},
		{"provider sends none", "sub-ada", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := externalIDConflict(User{ExternalID: tc.stored}, SSOIdentity{ExternalID: tc.asserted})
			if got != tc.want {
				t.Fatalf("externalIDConflict(%q, %q) = %v, want %v", tc.stored, tc.asserted, got, tc.want)
			}
		})
	}
}
