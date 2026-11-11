package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/lint"
)

func sample() []lint.Finding {
	two := int64(2)
	return []lint.Finding{
		{RuleID: "DRA001", Rule: "legacy-device-request", Severity: lint.Error, File: "deploy/app.yaml", Line: 17, Column: 13,
			Kind: "Deployment", Namespace: "ml", Name: "trainer", Container: "main", Resource: "nvidia.com/gpu", Count: &two,
			Message: `container "main": nvidia.com/gpu: 2 (limits) is a device-plugin request`, Fixable: true},
		{RuleID: "DRA104", Rule: "no-gpu-toleration", Severity: lint.Info, File: "deploy/app.yaml", Line: 9, Kind: "Deployment", Name: "x",
			Message: "100% sure,\nmultiline"},
		{RuleID: "DRA006", Rule: "shared-resource", Severity: lint.Warning, File: "cluster://ns/Pod/p", Message: "shared"},
	}
}

func render(t *testing.T, format string, fs []lint.Finding) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, format, fs, Meta{Version: "1.2.3", URL: "https://example.invalid"}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestText(t *testing.T) {
	out := render(t, "text", sample())
	for _, want := range []string{
		`deploy/app.yaml:17:13: error DRA001 Deployment/ml/trainer: container "main": nvidia.com/gpu: 2 (limits) is a device-plugin request [fixable]`,
		"deploy/app.yaml:9: info DRA104 Deployment/x: ",
		"cluster://ns/Pod/p: warning DRA006 shared",
		"1 error(s), 1 warning(s), 1 note(s); 1 fixable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output misses %q:\n%s", want, out)
		}
	}
	if out := render(t, "text", nil); !strings.Contains(out, "No legacy device requests found") {
		t.Errorf("empty text output: %q", out)
	}
}

func TestJSON(t *testing.T) {
	var got struct {
		Findings []lint.Finding
		Summary  lint.Summary
	}
	if err := json.Unmarshal([]byte(render(t, "json", sample())), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 3 || got.Summary.Errors != 1 || got.Findings[0].Count == nil || *got.Findings[0].Count != 2 {
		t.Errorf("unexpected JSON: %+v", got)
	}
	if out := render(t, "json", nil); !strings.Contains(out, `"findings": []`) {
		t.Errorf("empty JSON should have an empty array: %s", out)
	}
}

func TestGitHub(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(render(t, "github", sample())), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "::error file=deploy/app.yaml,line=17,col=13,title=DRA001 legacy-device-request::Deployment/ml/trainer: ") {
		t.Errorf("line 0: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "::notice file=deploy/app.yaml,line=9,title=") || !strings.HasSuffix(lines[1], "100%25 sure,%0Amultiline") {
		t.Errorf("line 1 (escaping): %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "::warning file=cluster%3A//ns/Pod/p,title=") {
		t.Errorf("line 2: %s", lines[2])
	}
}

func TestSARIF(t *testing.T) {
	var log struct {
		Version string
		Schema  string `json:"$schema"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name    string
					Version string
					Rules   []struct{ ID string }
				}
			}
			Results []struct {
				RuleID    string
				RuleIndex int
				Level     string
				Message   struct{ Text string }
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string }
						Region           *struct{ StartLine, StartColumn int }
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(render(t, "sarif", sample())), &log); err != nil {
		t.Fatal(err)
	}
	if log.Version != "2.1.0" || len(log.Runs) != 1 {
		t.Fatalf("bad SARIF envelope: %+v", log)
	}
	run := log.Runs[0]
	if run.Tool.Driver.Name != "dra-migrate" || run.Tool.Driver.Version != "1.2.3" || len(run.Tool.Driver.Rules) != len(lint.AllRules()) {
		t.Errorf("bad driver: %+v", run.Tool.Driver)
	}
	if len(run.Results) != 3 {
		t.Fatalf("got %d results", len(run.Results))
	}
	for _, r := range run.Results {
		if run.Tool.Driver.Rules[r.RuleIndex].ID != r.RuleID {
			t.Errorf("ruleIndex %d does not point at %s", r.RuleIndex, r.RuleID)
		}
	}
	r0 := run.Results[0]
	if r0.Level != "error" || r0.Locations[0].PhysicalLocation.Region.StartLine != 17 || r0.Locations[0].PhysicalLocation.ArtifactLocation.URI != "deploy/app.yaml" {
		t.Errorf("result 0: %+v", r0)
	}
	if run.Results[1].Level != "note" || run.Results[2].Locations[0].PhysicalLocation.Region != nil {
		t.Errorf("levels/regions wrong: %+v", run.Results)
	}
	if out := render(t, "sarif", nil); !strings.Contains(out, `"results": []`) {
		t.Error("empty SARIF must contain an empty results array")
	}
}

func TestUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, "xml", nil, Meta{}); err == nil {
		t.Error("expected an error")
	}
}
