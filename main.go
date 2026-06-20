// Command gander is a single-process, low-overhead latency diagnostic for Go.
//
// It captures the execution trace plus runtime and kernel/cgroup signals onto one
// correlated timeline, triggered on latency anomalies (flight-recorder style), and
// runs a deterministic rules engine to report what's wrong.
//
// Status: proof-of-concept skeleton. The CLI is a placeholder.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is the build version, overridable via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gander:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("gander", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "gander %s — latency diagnostic for Go (PoC)\n\n", version)
		fmt.Fprintf(fs.Output(), "usage: gander [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	fmt.Printf("gander %s — proof-of-concept skeleton; not yet implemented.\n", version)
	fmt.Println("The collector/synthesis and diagnosis layers are not built yet.")
	return nil
}
