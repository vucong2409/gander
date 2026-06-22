// Command gander is a single-process, low-overhead latency diagnostic for Go.
//
// It captures the execution trace plus runtime and kernel/cgroup signals onto
// one correlated timeline, triggered on latency anomalies (flight-recorder
// style), then runs a deterministic rules engine to report what's wrong. It also
// converts any Go 1.25 execution trace into the fused Perfetto view.
//
// See the per-command help (`gander <command> -h`) for usage.
package main

import "os"

// version is the build version, overridable via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	// cobra prints the error and (with SilenceUsage) skips the usage dump.
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
