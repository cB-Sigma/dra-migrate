package convert

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// An op is one structural edit of a document. Ops reference nodes of the
// original, unmodified tree so that they can be applied both to the tree and
// to the source text.
type opKind int

const (
	opRemovePair opKind = iota // remove key/val from mapping parent
	opAppendPair               // append key/val to mapping parent
	opAppendItem               // append val to sequence parent
)

type op struct {
	kind   opKind
	parent *yaml.Node
	key    *yaml.Node
	val    *yaml.Node
	// moveHead says that the comment above a removed key is re-attached to
	// a newly inserted node, so the original comment lines are removed too.
	moveHead bool
}

// applyToTree performs the ops on the node tree.
func applyToTree(ops []op) {
	for _, o := range ops {
		switch o.kind {
		case opRemovePair:
			yamlutil.Remove(o.parent, o.key)
		case opAppendPair:
			o.parent.Content = append(o.parent.Content, o.key, o.val)
		case opAppendItem:
			o.parent.Content = append(o.parent.Content, o.val)
		}
	}
}

// textPatcher applies ops to the source text of a document by deleting and
// inserting whole lines. Everything it does not touch stays byte-identical,
// which keeps comments, blank lines, quoting and indentation intact.
//
// It deliberately supports only block-style YAML laid out the way manifests
// are normally written. Anything else makes it give up (ok == false); the
// caller then falls back to re-encoding the node tree. Its result is never
// trusted blindly: the caller re-parses the patched text and compares it
// with the mutated tree.
type textPatcher struct {
	lines []string
	del   []bool
	ins   map[int][]string
	style yamlutil.Style
	crlf  bool
	ok    bool
}

func newTextPatcher(raw string, style yamlutil.Style) *textPatcher {
	p := &textPatcher{style: style, ok: true, ins: map[int][]string{}}
	p.crlf = strings.Contains(raw, "\r\n")
	p.lines = strings.Split(raw, "\n")
	p.del = make([]bool, len(p.lines))
	if strings.Contains(raw, "\t") {
		// Tabs are legal inside scalars but make column arithmetic unsafe.
		p.ok = false
	}
	return p
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// blockEnd returns the index of the last line belonging to the block that
// starts at line start and whose owner is indented by col spaces. Trailing
// blank and comment lines are not part of the block.
func (p *textPatcher) blockEnd(start, col int, seqValue bool) int {
	end := start
	for j := start + 1; j < len(p.lines); j++ {
		t := strings.TrimSpace(p.lines[j])
		if t == "" || t[0] == '#' {
			continue
		}
		ind := indentOf(p.lines[j])
		if ind > col || (seqValue && ind == col && (t == "-" || strings.HasPrefix(t, "- "))) {
			end = j
			continue
		}
		break
	}
	return end
}

func isBlockSeq(n *yaml.Node) bool {
	n = yamlutil.Resolve(n)
	return yamlutil.IsSeq(n) && !yamlutil.IsFlow(n) && len(n.Content) > 0
}

func (p *textPatcher) line(n *yaml.Node) (int, bool) {
	i := n.Line - 1
	if n.Line <= 0 || i >= len(p.lines) {
		return 0, false
	}
	return i, true
}

func (p *textPatcher) apply(o op) {
	if !p.ok {
		return
	}
	switch o.kind {
	case opRemovePair:
		p.ok = p.removePair(o)
	case opAppendPair:
		p.ok = p.appendPair(o)
	case opAppendItem:
		p.ok = p.appendItem(o)
	}
}

func (p *textPatcher) removePair(o op) bool {
	if yamlutil.IsFlow(o.parent) || o.key.Kind != yaml.ScalarNode {
		return false
	}
	kl, ok := p.line(o.key)
	if !ok || indentOf(p.lines[kl]) != o.key.Column-1 {
		return false
	}
	end := p.blockEnd(kl, o.key.Column-1, isBlockSeq(o.val))
	if o.moveHead && o.key.HeadComment != "" {
		// The comment above the key travels with the request; see convert.go.
		n := strings.Count(strings.TrimSpace(o.key.HeadComment), "\n") + 1
		for i := kl - n; i < kl; i++ {
			if i < 0 || !strings.HasPrefix(strings.TrimSpace(p.lines[i]), "#") {
				return false
			}
		}
		kl -= n
	}
	for i := kl; i <= end; i++ {
		p.del[i] = true
	}
	return true
}

func (p *textPatcher) render(n *yaml.Node, indent int) ([]string, bool) {
	text, err := yamlutil.Encode(n, p.style)
	if err != nil {
		return nil, false
	}
	pad := strings.Repeat(" ", indent)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			l = pad + l
		}
		if p.crlf {
			l += "\r"
		}
		out = append(out, l)
	}
	return out, true
}

func (p *textPatcher) appendPair(o op) bool {
	m := o.parent
	if yamlutil.IsFlow(m) || len(m.Content) < 2 {
		return false
	}
	col := m.Content[0].Column - 1
	lastKey, lastVal := m.Content[len(m.Content)-2], m.Content[len(m.Content)-1]
	kl, ok := p.line(lastKey)
	if !ok || lastKey.Column-1 != col {
		return false
	}
	end := p.blockEnd(kl, col, isBlockSeq(lastVal))
	pair := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{o.key, o.val}}
	lines, ok := p.render(pair, col)
	if !ok {
		return false
	}
	p.ins[end] = append(p.ins[end], lines...)
	return true
}

func (p *textPatcher) appendItem(o op) bool {
	s := o.parent
	if !isBlockSeq(s) {
		return false
	}
	fl, ok := p.line(s.Content[0])
	if !ok || !strings.HasPrefix(strings.TrimLeft(p.lines[fl], " "), "-") {
		return false
	}
	dash := indentOf(p.lines[fl])
	ll, ok := p.line(s.Content[len(s.Content)-1])
	if !ok || indentOf(p.lines[ll]) != dash || !strings.HasPrefix(strings.TrimLeft(p.lines[ll], " "), "-") {
		return false
	}
	end := p.blockEnd(ll, dash, false)
	lines, ok := p.render(yamlutil.Seq(o.val), dash)
	if !ok {
		return false
	}
	p.ins[end] = append(p.ins[end], lines...)
	return true
}

func (p *textPatcher) result() string {
	var out []string
	for i, l := range p.lines {
		if !p.del[i] {
			out = append(out, l)
		}
		out = append(out, p.ins[i]...)
	}
	return strings.Join(out, "\n")
}
