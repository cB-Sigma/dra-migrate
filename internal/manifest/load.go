// Package manifest loads Kubernetes YAML manifests as yaml.v3 node trees
// while keeping the original text of every document, and locates pod
// specifications and legacy device requests inside them.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// StdinName is the display name of manifests read from standard input.
const StdinName = "<stdin>"

// File is one input file split into YAML documents.
type File struct {
	// Path is the path as given by the user (or StdinName).
	Path string
	// Rel is the path relative to the argument it was discovered under; it
	// is used to mirror directory layouts with --output-dir.
	Rel string
	// Data is the original content.
	Data []byte
	// Segments are the documents in order.
	Segments []*Segment
	// HelmTemplate is set when at least one document is an unrendered Helm
	// (Go text/template) template rather than plain YAML.
	HelmTemplate bool
	// JSON is set for JSON input, which can be linted but not rewritten.
	JSON bool
}

// Segment is one YAML document together with its exact source text.
type Segment struct {
	// Raw is the source text including a leading "---" line, if any.
	Raw string
	// StartLine is the 1-based line number of the first line of Raw.
	StartLine int
	// Doc is the parsed document node, nil for empty or comment-only
	// documents and for documents that failed to parse.
	Doc *yaml.Node
	// Err is the parse error, if any.
	Err error
	// HelmTemplate is set when the document contains unrendered template
	// actions. Such documents are never analysed.
	HelmTemplate bool
	// Objects are the Kubernetes objects in the document: usually one, more
	// for a List, none if the document is not a Kubernetes object.
	Objects []*Object
}

// Line converts a node line (relative to the segment) to a file line.
func (s *Segment) Line(n *yaml.Node) int {
	if n == nil || n.Line == 0 {
		return s.StartLine
	}
	return s.StartLine + n.Line - 1
}

var yamlExt = map[string]bool{".yaml": true, ".yml": true, ".json": true}

// Load reads manifests from the given arguments. Each argument is a file, a
// directory (walked recursively for .yaml, .yml and .json files) or "-" for
// standard input. With no arguments standard input is read.
func Load(args []string, stdin io.Reader) ([]*File, error) {
	if len(args) == 0 {
		args = []string{"-"}
	}
	var files []*File
	seenStdin := false
	for _, arg := range args {
		if arg == "-" {
			if seenStdin {
				return nil, errors.New(`"-" (stdin) given more than once`)
			}
			seenStdin = true
			data, err := io.ReadAll(stdin)
			if err != nil {
				return nil, fmt.Errorf("reading stdin: %w", err)
			}
			files = append(files, Parse(StdinName, "stdin.yaml", data))
			continue
		}
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(arg)
			if err != nil {
				return nil, err
			}
			files = append(files, Parse(arg, filepath.Base(arg), data))
			continue
		}
		var paths []string
		err = filepath.WalkDir(arg, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != arg && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if yamlExt[strings.ToLower(filepath.Ext(p))] {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(paths)
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(arg, p)
			if err != nil {
				rel = filepath.Base(p)
			}
			files = append(files, Parse(p, rel, data))
		}
	}
	return files, nil
}

// looksTemplated reports whether a parsed document contains the tell-tale
// shape of an unquoted template action: "{{ .Values.x }}" parses as a flow
// mapping whose only key is itself a flow mapping. Template actions inside
// strings (alert rules, embedded config files) are ordinary scalars and do
// not match.
func looksTemplated(n *yaml.Node, depth int) bool {
	if n == nil || depth > maxDepth {
		return false
	}
	if n.Kind == yaml.MappingNode && yamlutil.IsFlow(n) && len(n.Content) >= 1 {
		if k := n.Content[0]; k.Kind == yaml.MappingNode && yamlutil.IsFlow(k) {
			return true
		}
	}
	for _, c := range n.Content {
		if looksTemplated(c, depth+1) {
			return true
		}
	}
	return false
}

// Parse splits data into documents and parses each of them.
func Parse(path, rel string, data []byte) *File {
	f := &File{Path: path, Rel: rel, Data: data}
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && !bytes.HasPrefix(trimmed, []byte("{{")) {
		f.JSON = true
	}
	for _, seg := range split(string(data)) {
		parseSegment(seg)
		if !f.JSON && strings.Contains(seg.Raw, "{{") && (seg.Err != nil || looksTemplated(seg.Doc, 0)) {
			seg.HelmTemplate, seg.Err, seg.Objects = true, nil, nil
			f.HelmTemplate = true
		}
		f.Segments = append(f.Segments, seg)
	}
	return f
}

func isSeparator(line string) bool {
	if !strings.HasPrefix(line, "---") {
		return false
	}
	rest := line[3:]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r'
}

// split cuts text into documents at "---" separator lines. Every byte of
// text ends up in exactly one segment.
func split(text string) []*Segment {
	var segs []*Segment
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	start := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		segs = append(segs, &Segment{Raw: strings.Join(lines[start:end], ""), StartLine: start + 1})
		start = end
	}
	for i, l := range lines {
		if i > 0 && isSeparator(strings.TrimRight(l, "\n")) {
			flush(i)
		}
	}
	flush(len(lines))
	return segs
}

func parseSegment(seg *Segment) {
	var doc yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(seg.Raw))
	if err := dec.Decode(&doc); err != nil {
		if !errors.Is(err, io.EOF) {
			seg.Err = err
		}
		return
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return
	}
	seg.Doc = &doc
	root := doc.Content[0]
	if !yamlutil.IsMap(root) {
		return
	}
	seg.Objects = objectsIn(root)
}
