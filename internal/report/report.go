// Package report renders lint findings in several output formats.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/cb-Sigma/dra-migrate/internal/lint"
)

// Formats lists the supported output formats.
var Formats = []string{"text", "json", "sarif", "github"}

// Meta carries tool information for formats that embed it.
type Meta struct {
	Version string
	URL     string
}

// Write renders findings in the given format.
func Write(w io.Writer, format string, findings []lint.Finding, meta Meta) error {
	switch format {
	case "text", "":
		return writeText(w, findings)
	case "json":
		return writeJSON(w, findings)
	case "sarif":
		return writeSARIF(w, findings, meta)
	case "github":
		return writeGitHub(w, findings)
	}
	return fmt.Errorf("unknown output format %q (want one of %s)", format, strings.Join(Formats, ", "))
}

func location(f lint.Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc += fmt.Sprintf(":%d", f.Line)
		if f.Column > 0 {
			loc += fmt.Sprintf(":%d", f.Column)
		}
	}
	return loc
}

func writeText(w io.Writer, findings []lint.Finding) error {
	for _, f := range findings {
		wl := f.Workload()
		if wl != "" {
			wl += ": "
		}
		fix := ""
		if f.Fixable {
			fix = " [fixable]"
		}
		if _, err := fmt.Fprintf(w, "%s: %s %s %s%s%s\n", location(f), f.Severity, f.RuleID, wl, f.Message, fix); err != nil {
			return err
		}
	}
	s := lint.Summarize(findings)
	if len(findings) == 0 {
		_, err := fmt.Fprintln(w, "No legacy device requests found.")
		return err
	}
	_, err := fmt.Fprintf(w, "\n%d error(s), %d warning(s), %d note(s); %d fixable with `dra-migrate convert`.\n",
		s.Errors, s.Warnings, s.Infos, s.Fixable)
	return err
}

func writeJSON(w io.Writer, findings []lint.Finding) error {
	if findings == nil {
		findings = []lint.Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Findings []lint.Finding `json:"findings"`
		Summary  lint.Summary   `json:"summary"`
	}{findings, lint.Summarize(findings)})
}

// escapeData escapes a GitHub Actions workflow command message.
func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// escapeProp escapes a GitHub Actions workflow command property value.
func escapeProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

func writeGitHub(w io.Writer, findings []lint.Finding) error {
	for _, f := range findings {
		cmd := "notice"
		switch f.Severity {
		case lint.Error:
			cmd = "error"
		case lint.Warning:
			cmd = "warning"
		}
		props := []string{"file=" + escapeProp(filepath.ToSlash(f.File))}
		if f.Line > 0 {
			props = append(props, fmt.Sprintf("line=%d", f.Line))
			if f.Column > 0 {
				props = append(props, fmt.Sprintf("col=%d", f.Column))
			}
		}
		props = append(props, "title="+escapeProp(f.RuleID+" "+f.Rule))
		msg := f.Message
		if wl := f.Workload(); wl != "" {
			msg = wl + ": " + msg
		}
		if _, err := fmt.Fprintf(w, "::%s %s::%s\n", cmd, strings.Join(props, ","), escapeData(msg)); err != nil {
			return err
		}
	}
	return nil
}

// SARIF 2.1.0 structures (only the parts we emit).
type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifRule struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	ShortDescription sarifText `json:"shortDescription"`
	FullDescription  sarifText `json:"fullDescription"`
	Help             sarifText `json:"help"`
	DefaultConfig    struct {
		Level string `json:"level"`
	} `json:"defaultConfiguration"`
}

type sarifResult struct {
	RuleID     string          `json:"ruleId"`
	RuleIndex  int             `json:"ruleIndex"`
	Level      string          `json:"level"`
	Message    sarifText       `json:"message"`
	Locations  []sarifLocation `json:"locations"`
	Properties map[string]any  `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation struct {
		URI string `json:"uri"`
	} `json:"artifactLocation"`
	Region *sarifRegion `json:"region,omitempty"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
}

func sarifLevel(s lint.Severity) string {
	switch s {
	case lint.Error:
		return "error"
	case lint.Warning:
		return "warning"
	}
	return "note"
}

func writeSARIF(w io.Writer, findings []lint.Finding, meta Meta) error {
	rules := lint.AllRules()
	index := map[string]int{}
	driver := sarifDriver{Name: "dra-migrate", Version: meta.Version, InformationURI: meta.URL}
	for i, r := range rules {
		index[r.ID] = i
		sr := sarifRule{ID: r.ID, Name: r.Name,
			ShortDescription: sarifText{r.Summary}, FullDescription: sarifText{r.Help}, Help: sarifText{r.Help}}
		sr.DefaultConfig.Level = sarifLevel(r.Severity)
		driver.Rules = append(driver.Rules, sr)
	}
	results := []sarifResult{}
	for _, f := range findings {
		msg := f.Message
		if wl := f.Workload(); wl != "" {
			msg = wl + ": " + msg
		}
		var phys sarifPhysical
		phys.ArtifactLocation.URI = filepath.ToSlash(f.File)
		if f.Line > 0 {
			phys.Region = &sarifRegion{StartLine: f.Line, StartColumn: f.Column}
		}
		res := sarifResult{
			RuleID: f.RuleID, RuleIndex: index[f.RuleID], Level: sarifLevel(f.Severity),
			Message:   sarifText{msg},
			Locations: []sarifLocation{{PhysicalLocation: phys}},
		}
		props := map[string]any{}
		if f.Container != "" {
			props["container"] = f.Container
		}
		if f.Resource != "" {
			props["resource"] = f.Resource
		}
		if f.Count != nil {
			props["count"] = *f.Count
		}
		if wl := f.Workload(); wl != "" {
			props["workload"] = wl
		}
		if len(props) > 0 {
			res.Properties = props
		}
		results = append(results, res)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(sarifLog{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs:    []sarifRun{{Tool: sarifTool{Driver: driver}, Results: results}},
	})
}
