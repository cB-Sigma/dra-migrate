package yamlutil

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func parse(t *testing.T, doc string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &n); err != nil {
		t.Fatal(err)
	}
	return n.Content[0]
}

func TestAccessors(t *testing.T) {
	root := parse(t, "a:\n  b:\n    c: hello\nlist: [1, 2]\nanchor: &x {k: v}\nalias: *x\n")
	if got := String(root, "a", "b", "c"); got != "hello" {
		t.Errorf("String = %q", got)
	}
	if String(root, "a", "missing") != "" || Path(root, "a", "b", "c", "d") != nil {
		t.Error("missing paths must yield zero values")
	}
	if !IsSeq(Value(root, "list")) || !IsFlow(Value(root, "list")) || !IsMap(Value(root, "a")) || IsFlow(Value(root, "a")) {
		t.Error("kind/style helpers are wrong")
	}
	if got := String(root, "alias", "k"); got != "v" {
		t.Errorf("aliases must be resolved, got %q", got)
	}
	if !HasAlias(root) || HasAlias(Value(root, "a")) {
		t.Error("HasAlias is wrong")
	}
	k, _ := Get(root, "list")
	if !Remove(root, k) || Value(root, "list") != nil || Remove(root, k) {
		t.Error("Remove is wrong")
	}
}

func TestEncodeStyles(t *testing.T) {
	n := Map("spec", Map("items", Seq(Map("name", "a", "count", Int("2")), Map("name", "1"))))
	compact, err := Encode(n, Style{Indent: 2, CompactSeq: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "spec:\n  items:\n  - name: a\n    count: 2\n  - name: \"1\"\n"
	if compact != want {
		t.Errorf("compact:\n%s\nwant:\n%s", compact, want)
	}
	indented, err := Encode(n, Style{Indent: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indented, "    items:\n        - name: a\n          count: 2\n") {
		t.Errorf("indented:\n%s", indented)
	}
	// Strings that look like other types must be quoted so that they stay
	// strings when the output is parsed again.
	back := parse(t, compact)
	if !Equal(back, n) {
		t.Errorf("round trip changed the document:\n%s", compact)
	}
}

func TestDetectStyle(t *testing.T) {
	tests := []struct {
		doc  string
		want Style
	}{
		{"a:\n  b:\n  - x\n", Style{Indent: 2, CompactSeq: true}},
		{"a:\n  b:\n    - x\n", Style{Indent: 2, CompactSeq: false}},
		{"a:\n    b:\n        - x\n", Style{Indent: 4, CompactSeq: false}},
		{"a:\n   b:\n   - x\n", Style{Indent: 3, CompactSeq: true}},
		{"a: {b: [x]}\n", DefaultStyle},
		{"a: 1\n", DefaultStyle},
	}
	for _, tt := range tests {
		got := DetectStyle(parse(t, tt.doc), strings.Split(tt.doc, "\n"))
		if got != tt.want {
			t.Errorf("DetectStyle(%q) = %+v, want %+v", tt.doc, got, tt.want)
		}
	}
}

func TestEqual(t *testing.T) {
	a := parse(t, "a: 1 # comment\nb:\n- x\n- 'y'\n")
	b := parse(t, "{a: 1, b: [x, y]}")
	if !Equal(a, b) {
		t.Error("style and comments must be ignored")
	}
	for _, other := range []string{"a: '1'\nb: [x, y]\n", "b: [x, y]\na: 1\n", "a: 1\nb: [x]\n", "a: 1\nb: {x: y}\n"} {
		if Equal(a, parse(t, other)) {
			t.Errorf("%q must not be equal", other)
		}
	}
	if !Equal(nil, nil) || Equal(a, nil) {
		t.Error("nil handling is wrong")
	}
}
