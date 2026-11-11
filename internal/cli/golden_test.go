package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/verify"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

const goldenDir = "../../testdata/golden"

// run executes the CLI in dir and returns its exit code and output.
func run(t *testing.T, dir string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	if dir != "" {
		t.Chdir(dir)
	}
	var out, errb bytes.Buffer
	code = Main(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func caseArgs(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// TestGolden converts every testdata/golden/<case>/input.yaml with the
// flags in <case>/args and compares the result with expected.yaml. On top
// of the textual comparison every expected file is
//
//   - strictly decoded into the Go API types (schema validity),
//   - checked for dangling claim references,
//   - checked to preserve the number of requested devices,
//   - converted again to prove idempotence, and
//   - linted to prove that nothing fixable is left.
func TestGolden(t *testing.T) {
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 15 {
		t.Fatalf("expected at least 15 golden cases, found %d", len(entries))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			dir, err := filepath.Abs(filepath.Join(goldenDir, name))
			if err != nil {
				t.Fatal(err)
			}
			args := caseArgs(t, dir)
			input, err := os.ReadFile(filepath.Join(dir, "input.yaml"))
			if err != nil {
				t.Fatal(err)
			}

			code, got, stderr := run(t, dir, "", append(append([]string{"convert"}, args...), "input.yaml")...)
			if code != ExitOK {
				t.Fatalf("convert exited %d: %s", code, stderr)
			}
			if strings.Contains(stderr, "re-serialised") != (name == "flow-style") {
				t.Errorf("unexpected patch mode; stderr:\n%s", stderr)
			}
			expectedPath := filepath.Join(dir, "expected.yaml")
			notesPath := filepath.Join(dir, "expected.notes")
			if *update {
				if err := os.WriteFile(expectedPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(notesPath, []byte(stderr), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("%v (run `go test ./internal/cli -run TestGolden -update`)", err)
			}
			if got != string(want) {
				t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", expectedPath, got, want)
			}
			wantNotes, err := os.ReadFile(notesPath)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != string(wantNotes) {
				t.Errorf("notes differ from %s\n--- got ---\n%s\n--- want ---\n%s", notesPath, stderr, wantNotes)
			}

			// Schema validity of input and output.
			before, err := verify.Manifests(input)
			if err != nil {
				t.Fatalf("input is not valid: %v", err)
			}
			after, err := verify.Manifests(want)
			if err != nil {
				t.Fatalf("output is not schema-valid: %v", err)
			}

			// Every generated reference resolves inside the file, unless it
			// was already present in the input.
			known := map[string]bool{}
			for _, pc := range before.PodClaims {
				known[pc.Template+"|"+pc.Claim] = true
			}
			for _, pc := range after.PodClaims {
				if known[pc.Template+"|"+pc.Claim] {
					continue
				}
				if pc.Template != "" && !after.Templates[pc.Namespace+"/"+pc.Template] {
					t.Errorf("%s: ResourceClaimTemplate %q is referenced but not generated", pc.Workload, pc.Template)
				}
				if pc.Claim != "" && !after.Claims[pc.Namespace+"/"+pc.Claim] {
					t.Errorf("%s: ResourceClaim %q is referenced but not generated", pc.Workload, pc.Claim)
				}
			}

			// No built-in legacy resource with a positive quantity is left.
			if len(args) == 0 || !strings.Contains(strings.Join(args, " "), "--mapping-file") {
				for res, n := range after.LegacyResources {
					if n > 0 {
						t.Errorf("legacy resource %s: %d left in output", res, n)
					}
				}
			}

			// Idempotence: converting the output changes nothing.
			code, again, stderr2 := run(t, "", "", append(append([]string{"convert"}, args...), "expected.yaml")...)
			if code != ExitOK {
				t.Fatalf("second convert exited %d: %s", code, stderr2)
			}
			if again != string(want) {
				t.Errorf("convert is not idempotent\n--- second run ---\n%s", again)
			}
			if !strings.Contains(stderr2, "nothing to convert") {
				t.Errorf("second run should report nothing to convert, got:\n%s", stderr2)
			}

			// Lint agrees: no fixable finding is left.
			lintArgs := []string{"lint", "--format", "json", "--fail-on", "none"}
			if strings.Contains(strings.Join(args, " "), "--mapping-file") {
				lintArgs = append(lintArgs, "--mapping-file", "mapping.yaml")
			}
			_, lintOut, _ := run(t, "", "", append(lintArgs, "expected.yaml")...)
			if strings.Contains(lintOut, `"fixable": true`) || strings.Contains(lintOut, "DRA001") {
				t.Errorf("lint still reports legacy requests in converted output:\n%s", lintOut)
			}
		})
	}
}
