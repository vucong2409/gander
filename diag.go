package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/collect"
	"github.com/vucong2409/gander/internal/diag"
	"github.com/vucong2409/gander/internal/synth"
)

// runDiag reads a capture bundle and prints scored findings — gander's "tell me
// what's wrong" layer. It writes findings.json beside the bundle.
//
//	gander diag bundles/20260101T000000.000-123
func runDiag(args []string) error {
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: gander diag <bundle-dir>")
	}
	dir := fs.Arg(0)

	pt, err := loadTrace(filepath.Join(dir, "trace.bin"))
	if err != nil {
		return err
	}
	proc := loadProc(filepath.Join(dir, "proc.json"))
	meta := loadMeta(filepath.Join(dir, "meta.json"))

	findings := diag.Diagnose(pt, proc, meta)
	if err := writeFindings(filepath.Join(dir, "findings.json"), findings); err != nil {
		return err
	}

	if len(findings) == 0 {
		fmt.Println("no findings — nothing exceeded its thresholds in this window")
		return nil
	}
	for _, f := range findings {
		fmt.Printf("[%-8s] %s\n            %s\n", f.Severity, f.Title, f.Evidence)
		if f.Suggestion != "" {
			fmt.Printf("            → %s\n", f.Suggestion)
		}
	}
	return nil
}

func loadTrace(path string) (*synth.ParsedTrace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return synth.ParseTrace(f)
}

// loadProc reads proc.json if present; it's an optional (Linux-only) collector.
func loadProc(path string) []collect.ProcSample {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s []collect.ProcSample
	_ = json.Unmarshal(b, &s)
	return s
}

func loadMeta(path string) bundle.Meta {
	var m bundle.Meta
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeFindings(path string, findings []diag.Finding) error {
	if findings == nil {
		findings = []diag.Finding{}
	}
	b, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
