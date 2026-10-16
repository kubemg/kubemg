package main

import "testing"

// The soft limit sits under the container's, so the runtime collects harder
// before the kernel kills the pod rather than after.
func TestSoftMemoryLimitSitsUnderTheContainers(t *testing.T) {
	got, ok := softMemoryLimit("268435456") // 256Mi, what the manifest sets
	if !ok {
		t.Fatal("a container limit must produce a soft limit")
	}
	if got != 214748364 {
		t.Fatalf("softMemoryLimit(256Mi) = %d, want 80%% of it", got)
	}
}

// No limit, or one that is not a number, leaves the runtime alone rather than
// setting a limit nobody asked for.
func TestSoftMemoryLimitIgnoresWhatIsNotALimit(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "256Mi", "lots"} {
		if _, ok := softMemoryLimit(raw); ok {
			t.Errorf("softMemoryLimit(%q) produced a limit", raw)
		}
	}
}
