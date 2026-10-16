package main

import "testing"

// The soft limit sits under the container's, so the runtime collects harder
// before the kernel kills the pod rather than after.
func TestSoftMemoryLimitSitsUnderTheContainers(t *testing.T) {
	got, ok := softMemoryLimit("536870912") // 512Mi, the chart's default
	if !ok {
		t.Fatal("a container limit must produce a soft limit")
	}
	if got != 429496728 {
		t.Fatalf("softMemoryLimit(512Mi) = %d, want 80%% of it", got)
	}
}

// No limit, or one that is not a number, leaves the runtime alone rather than
// setting a limit nobody asked for.
func TestSoftMemoryLimitIgnoresWhatIsNotALimit(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "512Mi", "lots"} {
		if _, ok := softMemoryLimit(raw); ok {
			t.Errorf("softMemoryLimit(%q) produced a limit", raw)
		}
	}
}
