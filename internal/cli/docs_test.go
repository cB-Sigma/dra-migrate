package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/verify"
)

var repoRoot, _ = filepath.Abs("../..")

const (
	diffBegin = "<!-- BEGIN DIFF: output of the command above, kept in sync by TestReadmeDiff -->\n```diff\n"
	diffEnd   = "```\n<!-- END DIFF -->"
)

// TestReadmeDiff keeps the before/after diff at the top of the README
// identical to what the tool really prints. Run with -update to refresh it.
func TestReadmeDiff(t *testing.T) {
	code, want, stderr := run(t, repoRoot, "", "convert", "--diff", "--quiet", "examples/before/02-deployment-multi-gpu.yaml")
	if code != ExitOK || want == "" {
		t.Fatalf("convert --diff failed (%d): %s", code, stderr)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	start := strings.Index(text, diffBegin)
	end := strings.Index(text, diffEnd)
	if start < 0 || end < start {
		t.Fatal("README.md does not contain the diff markers")
	}
	start += len(diffBegin)
	if *update {
		text = text[:start] + want + text[end:]
		if err := os.WriteFile("README.md", []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := text[start:end]; got != want {
		t.Errorf("README diff is stale; run `go test ./internal/cli -run TestReadmeDiff -update`\n--- README ---\n%s\n--- tool ---\n%s", got, want)
	}
}

// TestExamples makes sure examples/after is exactly what the tool produces
// from examples/before, and that it is valid and clean.
func TestExamples(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repoRoot, "examples", "before"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no examples found")
	}
	out := t.TempDir()
	code, _, stderr := run(t, repoRoot, "", "convert", "--quiet", "--output-dir", out, "examples/before")
	if code != ExitOK {
		t.Fatalf("convert failed (%d): %s", code, stderr)
	}
	for _, e := range entries {
		got, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("examples", "after", e.Name()))
		if err != nil {
			t.Fatalf("%v (run `make examples`)", err)
		}
		if string(got) != string(want) {
			t.Errorf("examples/after/%s is stale; run `make examples`", e.Name())
		}
		sum, err := verify.Manifests(want)
		if err != nil {
			t.Errorf("examples/after/%s is not schema-valid: %v", e.Name(), err)
			continue
		}
		if len(sum.LegacyResources) != 0 || len(sum.PodClaims) == 0 {
			t.Errorf("examples/after/%s: conversion incomplete: %+v", e.Name(), sum)
		}
	}
	after, err := os.ReadDir(filepath.Join("examples", "after"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(entries) {
		t.Errorf("examples/after has %d files, examples/before has %d", len(after), len(entries))
	}
	if code, out, _ := run(t, "", "", "lint", "examples/after"); code != ExitOK {
		t.Errorf("examples/after must lint clean:\n%s", out)
	}
	if code, _, _ := run(t, "", "", "lint", "examples/before"); code != ExitFindings {
		t.Errorf("examples/before must produce findings, exit code %d", code)
	}
	// The example mapping file must load.
	if code, _, stderr := run(t, "", "", "lint", "--mapping-file", "examples/mapping.yaml", "examples/after"); code != ExitOK {
		t.Errorf("examples/mapping.yaml: %s", stderr)
	}
}
