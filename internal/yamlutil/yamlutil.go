// Package yamlutil contains small helpers for working with yaml.v3 node trees.
package yamlutil

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Get returns the key and value nodes for key in mapping m, or nil, nil.
func Get(m *yaml.Node, key string) (k, v *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], Resolve(m.Content[i+1])
		}
	}
	return nil, nil
}

// Value returns the value node for key in mapping m, or nil.
func Value(m *yaml.Node, key string) *yaml.Node {
	_, v := Get(m, key)
	return v
}

// Path walks nested mappings and returns the node at the end of keys, or nil.
func Path(m *yaml.Node, keys ...string) *yaml.Node {
	cur := m
	for _, k := range keys {
		cur = Value(cur, k)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// String returns the scalar value at keys, or "".
func String(m *yaml.Node, keys ...string) string {
	n := Path(m, keys...)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// Resolve follows alias nodes.
func Resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// IsMap reports whether n is a mapping node.
func IsMap(n *yaml.Node) bool { return n != nil && n.Kind == yaml.MappingNode }

// IsSeq reports whether n is a sequence node.
func IsSeq(n *yaml.Node) bool { return n != nil && n.Kind == yaml.SequenceNode }

// IsFlow reports whether n is rendered in flow style ({...} or [...]).
func IsFlow(n *yaml.Node) bool { return n != nil && n.Style&yaml.FlowStyle != 0 }

// Scalar creates a plain string scalar node. Values that would otherwise be
// resolved as a non-string type are quoted by the encoder automatically.
func Scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// Int creates an integer scalar node.
func Int(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: v}
}

// Map creates a block mapping from alternating key, value arguments.
func Map(kv ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Content = append(m.Content, Scalar(kv[i].(string)), toNode(kv[i+1]))
	}
	return m
}

// Seq creates a block sequence from items.
func Seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func toNode(v any) *yaml.Node {
	switch t := v.(type) {
	case *yaml.Node:
		return t
	case string:
		return Scalar(t)
	}
	panic("yamlutil: unsupported value type")
}

// Append adds a key/value pair to mapping m.
func Append(m *yaml.Node, key string, v *yaml.Node) {
	m.Content = append(m.Content, Scalar(key), v)
}

// Remove deletes key from mapping m and reports whether it was present.
func Remove(m *yaml.Node, key *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i] == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// FromAny converts a decoded YAML/JSON value (maps, slices, scalars) into a
// node tree.
func FromAny(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}

// Style describes how a document is indented so that generated YAML can
// follow the conventions of the surrounding file.
type Style struct {
	Indent     int  // spaces per level
	CompactSeq bool // "- " items are not indented relative to their parent key
}

// DefaultStyle is the style used by kubectl and most Kubernetes manifests.
var DefaultStyle = Style{Indent: 2, CompactSeq: true}

// Encode renders n using the given style, without a document separator.
func Encode(n *yaml.Node, st Style) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if st.Indent <= 0 {
		st.Indent = 2
	}
	enc.SetIndent(st.Indent)
	if st.CompactSeq {
		enc.CompactSeqIndent()
	}
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// DetectStyle inspects the node tree and the original text lines (1-based
// node.Line indexes into lines) and returns the indentation style in use.
func DetectStyle(root *yaml.Node, lines []string) Style {
	st := DefaultStyle
	foundIndent, foundSeq := false, false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || (foundIndent && foundSeq) {
			return
		}
		if n.Kind == yaml.MappingNode && !IsFlow(n) {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if IsFlow(v) || len(v.Content) == 0 {
					continue
				}
				switch v.Kind {
				case yaml.MappingNode:
					if !foundIndent && v.Content[0].Line > k.Line {
						if d := v.Content[0].Column - k.Column; d > 0 {
							st.Indent, foundIndent = d, true
						}
					}
				case yaml.SequenceNode:
					if !foundSeq && v.Content[0].Line > k.Line {
						ln := v.Content[0].Line - 1
						if ln < len(lines) {
							line := lines[ln]
							ind := len(line) - len(strings.TrimLeft(line, " "))
							if strings.HasPrefix(strings.TrimLeft(line, " "), "-") {
								st.CompactSeq, foundSeq = ind == k.Column-1, true
							}
						}
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return st
}

// Equal reports whether two node trees are semantically identical: same
// structure, keys in the same order, and scalars with the same resolved tag
// and value. Comments, styles and positions are ignored.
func Equal(a, b *yaml.Node) bool {
	a, b = Resolve(a), Resolve(b)
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode {
		return a.ShortTag() == b.ShortTag() && a.Value == b.Value
	}
	for i := range a.Content {
		if !Equal(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// HasAlias reports whether the tree below n contains an alias node.
func HasAlias(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if HasAlias(c) {
			return true
		}
	}
	return false
}
