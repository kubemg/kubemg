package main

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// applyMemoryLimit tells the Go runtime about the container's memory limit.
//
// Without it the collector paces itself by heap growth alone, so a burst of
// wide list reads — each held whole while it is decoded — can take the heap
// past the limit before a collection is due, and the kernel OOM-kills the one
// replica every tunnel, terminal and browser session goes through. With a soft
// limit under the hard one, the runtime collects harder as it gets close
// instead: slower for a moment rather than gone.
//
// The limit arrives as KUBEMG_MEMORY_LIMIT_BYTES, which the chart fills from the
// container's own `limits.memory`, so the two cannot drift. An explicit
// GOMEMLIMIT wins: the runtime has already applied it, and an operator who set
// one meant it. The agent does the same (agent/cmd/agent).
func applyMemoryLimit(logger *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit, ok := softMemoryLimit(os.Getenv("KUBEMG_MEMORY_LIMIT_BYTES"))
	if !ok {
		return
	}
	debug.SetMemoryLimit(limit)
	logger.Info("memory limit applied", slog.Int64("soft_limit_bytes", limit))
}

// softMemoryLimit is the runtime's limit for a container limit of raw bytes:
// 80% of it, leaving the rest for what the runtime does not count against the
// heap — goroutine stacks, socket buffers, the binary itself.
func softMemoryLimit(raw string) (int64, bool) {
	limit, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || limit <= 0 {
		return 0, false
	}
	return limit / 5 * 4, true
}
