// Package convert rewrites workloads that use device-plugin extended
// resources into workloads that use DRA resource claims.
package convert

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/mapping"
	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// Claim modes.
const (
	ModeTemplate = "template"
	ModeClaim    = "claim"
)

// Options configure a conversion.
type Options struct {
	// APIVersion of resource.k8s.io to generate: v1 (default), v1beta1 or v1beta2.
	APIVersion string
	// ClaimMode is "template" (one ResourceClaim per pod, the default) or
	// "claim" (one ResourceClaim shared by every pod of the workload).
	ClaimMode string
	// ShareAcrossContainers makes all containers of a pod that request the
	// same resource share one claim, instead of one claim per container.
	ShareAcrossContainers bool
	// MapNodeSelectors moves GPU Feature Discovery device labels from
	// nodeSelector into CEL selectors on the device request.
	MapNodeSelectors bool
	// Mapper resolves resource names. Defaults to the built-in rules.
	Mapper *mapping.Mapper
}

func (o *Options) defaults() error {
	if o.APIVersion == "" {
		o.APIVersion = APIv1
	}
	o.APIVersion = strings.TrimPrefix(o.APIVersion, apiGroup+"/")
	switch o.APIVersion {
	case APIv1, APIv1beta1, APIv1beta2:
	default:
		return fmt.Errorf("unsupported API version %q (want v1, v1beta1 or v1beta2)", o.APIVersion)
	}
	if o.ClaimMode == "" {
		o.ClaimMode = ModeTemplate
	}
	switch o.ClaimMode {
	case ModeTemplate, ModeClaim:
	default:
		return fmt.Errorf("unsupported claim mode %q (want template or claim)", o.ClaimMode)
	}
	if o.Mapper == nil {
		o.Mapper = mapping.Default()
	}
	return nil
}

// Level of a note.
type Level string

// Note levels.
const (
	LevelWarning Level = "warning"
	LevelInfo    Level = "info"
)

// Note is a remark about a conversion that the user should read.
type Note struct {
	Level    Level
	File     string
	Line     int
	Workload string
	Message  string
}

func (n Note) String() string {
	loc := n.File
	if n.Line > 0 {
		loc = fmt.Sprintf("%s:%d", n.File, n.Line)
	}
	if n.Workload != "" {
		return fmt.Sprintf("%s: %s: %s: %s", loc, n.Level, n.Workload, n.Message)
	}
	return fmt.Sprintf("%s: %s: %s", loc, n.Level, n.Message)
}

// FileResult is the outcome for one input file.
type FileResult struct {
	File    *manifest.File
	Output  []byte
	Changed bool
	Notes   []Note
	// Containers is the number of containers rewritten.
	Containers int
	// Generated is the number of claim objects added to the file.
	Generated int
	// Reencoded is the number of documents that could not be patched
	// textually and were re-serialised from the node tree, which normalises
	// their formatting.
	Reencoded int
}

// Result is the outcome of a conversion run.
type Result struct {
	Files []*FileResult
}

// Changed reports whether any file changed.
func (r *Result) Changed() bool {
	for _, f := range r.Files {
		if f.Changed {
			return true
		}
	}
	return false
}

// Convert rewrites all files. Input files are not modified; the new content
// is returned in the result.
func Convert(files []*manifest.File, opts Options) (*Result, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}
	c := &converter{opts: opts, registry: map[string]*regEntry{}}
	for _, f := range files {
		c.seed(f)
	}
	res := &Result{}
	for _, f := range files {
		fr, err := c.file(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		res.Files = append(res.Files, fr)
	}
	return res, nil
}

type converter struct {
	opts Options
	// registry maps "Kind/namespace/name" of claim objects (existing in the
	// input or generated in this run) to a fingerprint of their spec.
	registry map[string]*regEntry
}

type regEntry struct {
	fingerprint string
	// seeded is set for objects that were already present in the input.
	seeded bool
	// files lists the files an object generated in this run was written to.
	// Every file that needs the object gets its own copy, so that each
	// output file can be applied on its own.
	files map[string]bool
}

func regKey(kind, ns, name string) string { return kind + "/" + ns + "/" + name }

func fingerprint(spec *yaml.Node) string {
	s, err := yamlutil.Encode(spec, yamlutil.DefaultStyle)
	if err != nil {
		return ""
	}
	return s
}

// seed records claim objects already present in the input so that generated
// names never collide with them and identical ones are reused.
func (c *converter) seed(f *manifest.File) {
	for _, seg := range f.Segments {
		for _, o := range seg.Objects {
			if !strings.HasPrefix(o.APIVersion, apiGroup+"/") || o.Name == "" {
				continue
			}
			var spec *yaml.Node
			switch o.Kind {
			case KindTemplate:
				spec = yamlutil.Path(o.Root, "spec", "spec")
			case KindClaim:
				spec = yamlutil.Value(o.Root, "spec")
			default:
				continue
			}
			fp := "<unknown>"
			if spec != nil {
				fp = fingerprint(stripComments(spec))
			}
			c.registry[regKey(o.Kind, o.Namespace, o.Name)] = &regEntry{fingerprint: fp, seeded: true}
		}
	}
}

func stripComments(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.HeadComment, cp.LineComment, cp.FootComment = "", "", ""
	cp.Style = 0
	cp.Content = nil
	for _, ch := range n.Content {
		cp.Content = append(cp.Content, stripComments(ch))
	}
	return &cp
}

func (c *converter) file(f *manifest.File) (*FileResult, error) {
	fr := &FileResult{File: f, Output: f.Data}
	note := func(level Level, line int, workload, format string, args ...any) {
		fr.Notes = append(fr.Notes, Note{Level: level, File: f.Path, Line: line, Workload: workload, Message: fmt.Sprintf(format, args...)})
	}
	if f.HelmTemplate {
		note(LevelWarning, 0, "", "unrendered Helm template left untouched; render the chart and convert the output, then port the change back to the template by hand")
		return fr, nil
	}
	var out strings.Builder
	for i, seg := range f.Segments {
		text := seg.Raw
		var generated []string
		if seg.Err == nil && seg.Doc != nil {
			sc := &segConv{c: c, f: f, fr: fr, seg: seg, note: note}
			var err error
			text, generated, err = sc.run()
			if err != nil {
				return nil, err
			}
		}
		if len(generated) == 0 {
			out.WriteString(text)
			continue
		}
		fr.Generated += len(generated)
		firstLine, _, _ := strings.Cut(text, "\n")
		if isSeparatorLine(firstLine) {
			for _, g := range generated {
				out.WriteString("---" + eol(g))
				out.WriteString(g)
			}
			out.WriteString(text)
			continue
		}
		// A document without a leading separator is the first in the file.
		// Keep a file header comment (a leading comment block followed by a
		// blank line) on top.
		header, rest := splitHeader(text)
		if i > 0 && out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
		out.WriteString(header)
		for _, g := range generated {
			out.WriteString(g)
			out.WriteString("---" + eol(g))
		}
		out.WriteString(rest)
	}
	if out.String() != string(f.Data) {
		fr.Output = []byte(out.String())
		fr.Changed = true
	}
	return fr, nil
}

// eol returns the line ending used by text.
func eol(text string) string {
	if strings.HasSuffix(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func isSeparatorLine(l string) bool {
	l = strings.TrimRight(l, "\r")
	return l == "---" || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "---\t")
}

// splitHeader splits off a leading comment block that is followed by a
// blank line.
func splitHeader(text string) (header, rest string) {
	lines := strings.SplitAfter(text, "\n")
	lastBlank := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			lastBlank = i
			continue
		}
		if strings.HasPrefix(t, "#") {
			continue
		}
		break
	}
	if lastBlank < 0 {
		return "", text
	}
	return strings.Join(lines[:lastBlank+1], ""), strings.Join(lines[lastBlank+1:], "")
}

// segConv converts one document.
type segConv struct {
	c    *converter
	f    *manifest.File
	fr   *FileResult
	seg  *manifest.Segment
	note func(level Level, line int, workload, format string, args ...any)

	ops       []op
	generated []*yaml.Node
}

// use is one container's use of a legacy resource.
type use struct {
	ctr   *manifest.Container
	entry *manifest.ResourceEntry
	count int64
}

// plan is one claim to be added to a pod.
type plan struct {
	res        *mapping.Resolved
	count      int64
	selectors  []string
	users      []*use
	owner      *manifest.Container // first app container using the claim
	podClaim   string
	objectName string
}

func (s *segConv) run() (text string, generated []string, err error) {
	seg := s.seg
	for _, obj := range seg.Objects {
		s.object(obj)
	}
	if len(s.ops) == 0 {
		return seg.Raw, nil, nil
	}
	lines := strings.Split(seg.Raw, "\n")
	style := yamlutil.DetectStyle(seg.Doc, lines)

	patcher := newTextPatcher(seg.Raw, style)
	for _, o := range s.ops {
		patcher.apply(o)
	}
	applyToTree(s.ops)

	text = ""
	if patcher.ok {
		candidate := patcher.result()
		var doc yaml.Node
		if yaml.Unmarshal([]byte(candidate), &doc) == nil && len(doc.Content) == 1 && yamlutil.Equal(doc.Content[0], seg.Doc.Content[0]) {
			text = candidate
		}
	}
	if text == "" {
		// The document is laid out in a way the text patcher does not
		// handle (flow style, tabs, ...). Re-encode it from the tree.
		enc, err := yamlutil.Encode(seg.Doc, style)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: re-encoding document: %w", seg.StartLine, err)
		}
		firstLine, _, _ := strings.Cut(seg.Raw, "\n")
		if isSeparatorLine(firstLine) {
			enc = firstLine + "\n" + enc
		}
		text = enc
		s.fr.Reencoded++
		s.note(LevelInfo, seg.StartLine, "", "document uses YAML that cannot be patched in place (flow style or unusual layout); it was re-serialised, so formatting may differ")
	}
	for _, g := range s.generated {
		enc, err := yamlutil.Encode(g, style)
		if err != nil {
			return "", nil, err
		}
		if patcher.crlf {
			enc = strings.ReplaceAll(enc, "\n", "\r\n")
		}
		generated = append(generated, enc)
	}
	return text, generated, nil
}

func (s *segConv) object(obj *manifest.Object) {
	wl := obj.DisplayName()
	line := s.seg.Line(obj.Root)
	m := s.c.opts.Mapper
	hasLegacy := false
	for _, ps := range obj.PodSpecs {
		for _, ctr := range ps.Containers {
			for _, e := range ctr.Entries(func(n string) bool { _, ok := m.Resolve(n); return ok }) {
				if n, ok := e.Count(); !ok || n > 0 {
					hasLegacy = true
				}
			}
		}
	}
	if !hasLegacy {
		return
	}
	if s.f.JSON {
		s.note(LevelWarning, line, wl, "JSON manifests are not rewritten; convert the YAML source instead")
		return
	}
	if obj.InList {
		s.note(LevelWarning, line, wl, "objects inside a List are not rewritten; split the List into separate documents first")
		return
	}
	if !manifest.IsBuiltinWorkload(obj.APIVersion, obj.Kind) {
		s.note(LevelWarning, line, wl, "pod template inside custom resource %s was found by structure; verify that its CRD schema preserves resourceClaims and resources.claims", obj.Kind)
	}
	for _, ps := range obj.PodSpecs {
		if yamlutil.HasAlias(ps.Node) {
			s.note(LevelWarning, s.seg.Line(ps.Node), wl, "pod template uses YAML anchors/aliases, which cannot be rewritten safely; expand them and convert again")
			continue
		}
		s.podSpec(obj, ps)
	}
}

func (s *segConv) podSpec(obj *manifest.Object, ps *manifest.PodSpec) {
	opts := s.c.opts
	m := opts.Mapper
	wl := obj.DisplayName()
	mapped := func(n string) bool { _, ok := m.Resolve(n); return ok }

	var plans []*plan
	var apps, inits []*use
	resolved := map[*use]*mapping.Resolved{}
	for _, ctr := range ps.Containers {
		for _, e := range ctr.Entries(mapped) {
			n, ok := e.Count()
			line := s.seg.Line(e.Primary().Key)
			if !ok {
				s.note(LevelWarning, line, wl, "container %q: %s has a quantity that is not a whole number; left untouched", ctr.Name, e.Resource)
				continue
			}
			if n == 0 {
				continue
			}
			if e.Limit != nil && e.Request != nil && (!e.Request.Valid || e.Request.Count != e.Limit.Count) {
				s.note(LevelWarning, line, wl, "container %q: %s request and limit differ; using the limit (%d)", ctr.Name, e.Resource, n)
			}
			u := &use{ctr: ctr, entry: e, count: n}
			res, _ := m.Resolve(e.Resource)
			resolved[u] = res
			if ctr.Init && !ctr.Sidecar {
				inits = append(inits, u)
			} else {
				apps = append(apps, u)
			}
		}
	}
	if len(apps)+len(inits) == 0 {
		return
	}

	effective := func(u *use) int64 {
		if resolved[u].CountMode == mapping.CountSingle {
			return 1
		}
		return u.count
	}
	byResource := map[string]*plan{}
	for _, u := range apps {
		res := resolved[u]
		if opts.ShareAcrossContainers {
			if p := byResource[res.Resource]; p != nil {
				p.users = append(p.users, u)
				if n := effective(u); n > p.count {
					p.count = n
				}
				continue
			}
		}
		p := &plan{res: res, count: effective(u), users: []*use{u}, owner: u.ctr}
		byResource[res.Resource] = p
		plans = append(plans, p)
	}
	for _, u := range inits {
		res := resolved[u]
		need := effective(u)
		var host *plan
		for _, p := range plans {
			if p.res.Resource != res.Resource {
				continue
			}
			if opts.ShareAcrossContainers || p.count >= need {
				host = p
				break
			}
		}
		line := s.seg.Line(u.entry.Primary().Key)
		if host != nil {
			if need > host.count {
				host.count = need
			}
			host.users = append(host.users, u)
			s.note(LevelInfo, line, wl, "init container %q reuses the claim of container %q (claims are allocated for the whole pod, so a separate claim would hold extra devices)", u.ctr.Name, host.owner.Name)
			continue
		}
		p := &plan{res: res, count: need, users: []*use{u}, owner: u.ctr}
		plans = append(plans, p)
		s.note(LevelWarning, line, wl, "init container %q gets its own claim for %s; unlike a device-plugin request the device stays allocated after the init container exits", u.ctr.Name, res.Resource)
	}

	// Optionally turn device-describing node labels into CEL selectors.
	var nsRemove []*yaml.Node
	nodeSel := yamlutil.Value(ps.Node, "nodeSelector")
	if opts.MapNodeSelectors && yamlutil.IsMap(nodeSel) {
		for i := 0; i+1 < len(nodeSel.Content); i += 2 {
			k, v := nodeSel.Content[i], yamlutil.Resolve(nodeSel.Content[i+1])
			if !mapping.DeviceLabel(k.Value) || v == nil || v.Kind != yaml.ScalarNode {
				continue
			}
			expr, ok := mapping.SelectorForNodeLabel(k.Value, v.Value)
			if !ok {
				s.note(LevelWarning, s.seg.Line(k), wl, "nodeSelector %s=%s could not be translated to a CEL selector; left in place", k.Value, v.Value)
				continue
			}
			applied := false
			for _, p := range plans {
				switch {
				case p.res.Kind == mapping.KindGPU,
					p.res.Kind == mapping.KindMIG && k.Value != mapping.LabelGPUMemory:
					p.selectors = append(p.selectors, expr)
					applied = true
				}
			}
			if applied {
				nsRemove = append(nsRemove, k)
				s.note(LevelWarning, s.seg.Line(k), wl, "nodeSelector %s=%s became CEL selector %q; GFD label values and DRA device attributes are not guaranteed to match, compare with `kubectl get resourceslices -o yaml`", k.Value, v.Value, expr)
			}
		}
	}

	s.namePodClaims(ps, plans)
	kind := KindTemplate
	refField := "resourceClaimTemplateName"
	if opts.ClaimMode == ModeClaim {
		kind, refField = KindClaim, "resourceClaimName"
	}

	var podItems []*yaml.Node
	for _, p := range plans {
		sels := append(append([]string(nil), p.res.Selectors...), p.selectors...)
		spec := claimSpec(opts.APIVersion, deviceRequest{res: p.res, count: p.count, selectors: sels})
		name, isNew := s.objectName(obj, p, kind, fingerprint(spec))
		p.objectName = name
		if isNew {
			s.generated = append(s.generated, claimObject(opts.APIVersion, kind, name, obj.Namespace, spec))
		}
		podItems = append(podItems, yamlutil.Map("name", p.podClaim, refField, name))
		s.planNotes(obj, p, kind)
	}

	// Container edits.
	perContainer := map[*manifest.Container][]*plan{}
	var order []*manifest.Container
	for _, p := range plans {
		for _, u := range p.users {
			if _, seen := perContainer[u.ctr]; !seen {
				order = append(order, u.ctr)
			}
			perContainer[u.ctr] = append(perContainer[u.ctr], p)
		}
	}
	for _, ctr := range order {
		s.fr.Containers++
		resources := ctr.Resources()
		remove := map[*yaml.Node][]*manifest.Quantity{} // limits/requests map -> entries
		var sides []*yaml.Node
		moved := map[*yaml.Node]bool{}
		items := yamlutil.Seq()
		for _, p := range perContainer[ctr] {
			for _, u := range p.users {
				if u.ctr != ctr {
					continue
				}
				item := yamlutil.Map("name", p.podClaim)
				for _, q := range []*manifest.Quantity{u.entry.Limit, u.entry.Request} {
					if q == nil {
						continue
					}
					if _, seen := remove[q.Map]; !seen {
						sides = append(sides, q.Map)
					}
					remove[q.Map] = append(remove[q.Map], q)
					// Comments attached to the removed entry move to the claim
					// reference that replaces it.
					if q.Val != nil && q.Val.LineComment != "" && item.Content[1].LineComment == "" {
						item.Content[1].LineComment = q.Val.LineComment
					}
					if q.Key.HeadComment != "" && item.HeadComment == "" {
						item.HeadComment = strings.TrimSpace(strings.ReplaceAll(q.Key.HeadComment, "\r", ""))
						moved[q.Key] = true
					}
				}
				items.Content = append(items.Content, item)
			}
		}
		for _, side := range sides {
			qs := remove[side]
			if len(qs)*2 == len(side.Content) {
				// Every entry goes away: drop "limits:"/"requests:" itself.
				for i := 0; i+1 < len(resources.Content); i += 2 {
					if yamlutil.Resolve(resources.Content[i+1]) == side {
						s.ops = append(s.ops, op{kind: opRemovePair, parent: resources, key: resources.Content[i], val: side})
					}
				}
				continue
			}
			for _, q := range qs {
				s.ops = append(s.ops, op{kind: opRemovePair, parent: side, key: q.Key, val: q.Val, moveHead: moved[q.Key]})
			}
		}
		if existing := yamlutil.Value(resources, "claims"); yamlutil.IsSeq(existing) {
			for _, it := range items.Content {
				s.ops = append(s.ops, op{kind: opAppendItem, parent: existing, val: it})
			}
		} else {
			s.ops = append(s.ops, op{kind: opAppendPair, parent: resources, key: yamlutil.Scalar("claims"), val: items})
		}
	}

	// Pod-level edits.
	if len(nsRemove) > 0 && len(nsRemove)*2 == len(nodeSel.Content) {
		k, _ := yamlutil.Get(ps.Node, "nodeSelector")
		s.ops = append(s.ops, op{kind: opRemovePair, parent: ps.Node, key: k, val: nodeSel})
	} else {
		for _, k := range nsRemove {
			_, v := yamlutil.Get(nodeSel, k.Value)
			s.ops = append(s.ops, op{kind: opRemovePair, parent: nodeSel, key: k, val: v})
		}
	}
	if existing := yamlutil.Value(ps.Node, "resourceClaims"); yamlutil.IsSeq(existing) {
		for _, it := range podItems {
			s.ops = append(s.ops, op{kind: opAppendItem, parent: existing, val: it})
		}
	} else {
		s.ops = append(s.ops, op{kind: opAppendPair, parent: ps.Node, key: yamlutil.Scalar("resourceClaims"), val: yamlutil.Seq(podItems...)})
	}
}

// namePodClaims assigns pod-level claim names that are unique within the pod.
func (s *segConv) namePodClaims(ps *manifest.PodSpec, plans []*plan) {
	taken := map[string]bool{}
	for _, n := range ps.ClaimNames() {
		taken[n] = true
	}
	perBase := map[string]int{}
	for _, p := range plans {
		perBase[p.res.ClaimName]++
	}
	for _, p := range plans {
		name := p.res.ClaimName
		if perBase[name] > 1 {
			name = mapping.SanitizeLabel(name+"-"+p.owner.Name, name)
		}
		p.podClaim = unique(name, 63, func(n string) bool { return taken[n] })
		taken[p.podClaim] = true
	}
}

// unique returns name, or name with a numeric suffix, such that used
// reports false, never exceeding limit characters.
func unique(name string, limit int, used func(string) bool) string {
	if len(name) > limit {
		name = strings.TrimRight(name[:limit], "-.")
	}
	if !used(name) {
		return name
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		base := name
		if len(base)+len(suffix) > limit {
			base = strings.TrimRight(base[:limit-len(suffix)], "-.")
		}
		if cand := base + suffix; !used(cand) {
			return cand
		}
	}
}

// objectName picks the name of the claim object for a plan and reports
// whether the object has to be written to the current file. Plans with an
// identical spec in the same namespace share one object.
func (s *segConv) objectName(obj *manifest.Object, p *plan, kind, fp string) (name string, emit bool) {
	reg := s.c.registry
	file := s.f.Path
	claim := func(n string) (ok, emit bool) {
		key := regKey(kind, obj.Namespace, n)
		e := reg[key]
		switch {
		case e == nil:
			reg[key] = &regEntry{fingerprint: fp, files: map[string]bool{file: true}}
			return true, true
		case e.fingerprint != fp:
			return false, false
		case e.seeded || e.files[file]:
			return true, false
		default:
			e.files[file] = true
			return true, true
		}
	}
	base := obj.BaseName()
	candidates := []string{base + "-" + p.res.ClaimName}
	if p.podClaim != p.res.ClaimName {
		candidates = append(candidates, base+"-"+p.podClaim)
	}
	for _, cand := range candidates {
		cand = unique(cand, 253, func(string) bool { return false })
		if ok, emit := claim(cand); ok {
			return cand, emit
		}
	}
	// Both candidates are taken by objects with a different spec: append a
	// counter, still reusing an identical object if one turns up.
	name = unique(candidates[0], 253, func(n string) bool {
		var ok bool
		ok, emit = claim(n)
		return !ok
	})
	return name, emit
}

func (s *segConv) planNotes(obj *manifest.Object, p *plan, kind string) {
	wl := obj.DisplayName()
	line := s.seg.Line(p.users[0].entry.Primary().Key)
	res := p.res
	for _, n := range res.Notes {
		s.note(LevelInfo, line, wl, "%s", n)
	}
	if res.Shared {
		cfg := "no sharing configuration was added (see --sharing)"
		if res.Config != nil {
			cfg = fmt.Sprintf("added %v sharing configuration, which needs the matching alpha feature gate in the NVIDIA DRA driver", res.Config.Parameters["sharing"].(map[string]any)["strategy"])
		}
		if n := p.users[0].count; n > 1 {
			cfg += fmt.Sprintf("; the quantity %d counted replicas of a device, a single device is requested", n)
		}
		if kind == KindTemplate {
			s.note(LevelWarning, line, wl, "%s was oversubscribed by the device plugin; the generated %s gives every pod its own device. To share one device between pods, reference a single ResourceClaim (--claim-mode claim); %s", res.Resource, kind, cfg)
		} else {
			s.note(LevelWarning, line, wl, "%s was oversubscribed by the device plugin; all pods now share the device(s) of ResourceClaim %s; %s", res.Resource, p.objectName, cfg)
		}
	}
	if res.Kind == mapping.KindMIG {
		s.note(LevelInfo, line, wl, "MIG profile %s is selected with a CEL selector on DeviceClass %s; the nodes must publish matching MIG devices (static MIG, or the driver's alpha DynamicMIG feature gate)", res.MIGProfile, res.DeviceClass)
	}
	if kind == KindClaim && !res.Shared {
		s.note(LevelWarning, line, wl, "ResourceClaim %s is shared by every pod that references it: all replicas of this workload get the same device(s)", p.objectName)
	}
	if len(p.users) > 1 {
		var names []string
		for _, u := range p.users {
			names = append(names, u.ctr.Name)
		}
		if s.c.opts.ShareAcrossContainers {
			s.note(LevelInfo, line, wl, "containers %s share claim %q (%d device(s)); with the device plugin each had its own", strings.Join(names, ", "), p.podClaim, p.count)
		}
	}
}
