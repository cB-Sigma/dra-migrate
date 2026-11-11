// Package lint reports legacy device-plugin style requests and related
// settings that need attention when migrating a workload to DRA.
package lint

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/mapping"
	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// Severity of a finding.
type Severity string

// Severities, from most to least severe.
const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Info    Severity = "info"
)

// Rank orders severities; higher is more severe.
func (s Severity) Rank() int {
	switch s {
	case Error:
		return 3
	case Warning:
		return 2
	case Info:
		return 1
	}
	return 0
}

// ParseSeverity parses a --fail-on value. "none" yields an empty severity.
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(s) {
	case "error":
		return Error, nil
	case "warning", "warn":
		return Warning, nil
	case "info":
		return Info, nil
	case "none", "never":
		return "", nil
	}
	return "", fmt.Errorf("unknown severity %q (want error, warning, info or none)", s)
}

// Rule describes a check.
type Rule struct {
	ID       string
	Name     string
	Severity Severity
	Summary  string
	Help     string
}

// Rules, indexed by ID.
var (
	RuleLegacyRequest = Rule{"DRA001", "legacy-device-request", Error,
		"Container requests a device through a device-plugin extended resource",
		"Replace the extended resource with a ResourceClaimTemplate (or ResourceClaim), a pod-level spec.resourceClaims entry and a container-level resources.claims reference. `dra-migrate convert` does this automatically."}
	RuleMismatch = Rule{"DRA002", "requests-limits-mismatch", Error,
		"Extended resource requests and limits disagree",
		"Extended resources cannot be overcommitted: the API server rejects pods whose request differs from the limit, or that set a request without a limit. Fix the manifest before migrating; convert uses the limit."}
	RuleBadQuantity = Rule{"DRA003", "unparsable-quantity", Warning,
		"Extended resource quantity is not a whole number",
		"The quantity could not be interpreted as a device count, so the request is not converted automatically."}
	RuleZero = Rule{"DRA004", "zero-quantity", Info,
		"Extended resource is requested with quantity 0",
		"A zero quantity allocates nothing and is left untouched by convert. Remove it once the device plugin is gone."}
	RuleUnmapped = Rule{"DRA005", "unmapped-resource", Warning,
		"Vendor resource has no DRA mapping",
		"dra-migrate does not know which DeviceClass replaces this resource. If it is an accelerator, describe it in a --mapping-file."}
	RuleShared = Rule{"DRA006", "shared-resource", Warning,
		"Oversubscribed (time-sliced/MPS) resource changes meaning under DRA",
		"A '.shared' resource counts replicas of a device. DRA has no replica counting: pods share a device by referencing the same ResourceClaim, and time-slicing/MPS are per-claim driver configuration (alpha feature gates in the NVIDIA driver). A ResourceClaimTemplate gives every pod its own, exclusive device."}
	RuleInit = Rule{"DRA007", "init-container-request", Warning,
		"Init container requests a device",
		"With device plugins, init container requests are not added to the pod's total. A DRA claim is allocated for the whole lifetime of the pod, so convert lets the init container reuse an app container's claim when one fits and only otherwise creates a separate claim."}
	RuleNodeSelector = Rule{"DRA101", "gpu-node-selector", Info,
		"nodeSelector uses an NVIDIA node label",
		"Labels that describe the device (gpu.product, gpu.memory, gpu.family) can become CEL selectors on the device request, see `convert --map-node-selectors`. Labels that describe the node keep working as long as GPU Feature Discovery is deployed."}
	RuleNodeAffinity = Rule{"DRA102", "gpu-node-affinity", Info,
		"Node affinity uses an NVIDIA node label",
		"Node affinity is never rewritten automatically. Consider expressing device constraints as CEL selectors on the device request instead."}
	RuleRuntimeClass = Rule{"DRA103", "nvidia-runtime-class", Info,
		"Pod sets an NVIDIA runtimeClassName",
		"The NVIDIA DRA driver injects devices through CDI and its own examples do not set a RuntimeClass. Check whether your nodes still need it before removing it."}
	RuleToleration = Rule{"DRA104", "no-gpu-toleration", Info,
		"Pod has no toleration for the nvidia.com/gpu taint",
		"Clusters that taint GPU nodes often rely on the ExtendedResourceToleration admission plugin, which only adds tolerations to pods that request extended resources. Pods that use resource claims need an explicit toleration."}
	RuleVisibleDevices = Rule{"DRA105", "visible-devices-env", Warning,
		"Container sets NVIDIA_VISIBLE_DEVICES",
		"Setting NVIDIA_VISIBLE_DEVICES by hand bypasses device allocation with the NVIDIA container runtime. Under DRA the driver decides which devices are injected; remove the variable."}
	RuleCustomKind = Rule{"DRA106", "custom-resource-pod-template", Info,
		"Pod template is embedded in a custom resource",
		"The pod template was found by structure, not by a known schema. Make sure the CRD's schema preserves spec.resourceClaims and containers[].resources.claims (CRDs generated against old Kubernetes libraries may prune them)."}
	RuleHelm = Rule{"DRA900", "helm-template", Info,
		"Unrendered Helm template skipped",
		"dra-migrate does not parse Go templates. Render the chart first: `helm template . | dra-migrate lint -`."}
	RuleParse = Rule{"DRA901", "parse-error", Error,
		"YAML could not be parsed",
		"Fix the syntax error; nothing in this document was analysed."}
)

// AllRules lists every rule in ID order.
func AllRules() []Rule {
	return []Rule{RuleLegacyRequest, RuleMismatch, RuleBadQuantity, RuleZero, RuleUnmapped, RuleShared, RuleInit,
		RuleNodeSelector, RuleNodeAffinity, RuleRuntimeClass, RuleToleration, RuleVisibleDevices, RuleCustomKind,
		RuleHelm, RuleParse}
}

// Finding is one reported problem.
type Finding struct {
	RuleID    string   `json:"ruleId"`
	Rule      string   `json:"rule"`
	Severity  Severity `json:"severity"`
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	Column    int      `json:"column,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name,omitempty"`
	Path      string   `json:"path,omitempty"`
	Container string   `json:"container,omitempty"`
	Resource  string   `json:"resource,omitempty"`
	Count     *int64   `json:"count,omitempty"`
	Message   string   `json:"message"`
	Fixable   bool     `json:"fixable"`
}

// Workload returns "Kind/name" or "Kind/namespace/name", or "".
func (f Finding) Workload() string {
	if f.Kind == "" {
		return ""
	}
	name := f.Name
	if name == "" {
		name = "<unnamed>"
	}
	if f.Namespace != "" {
		return f.Kind + "/" + f.Namespace + "/" + name
	}
	return f.Kind + "/" + name
}

// Options configure a lint run.
type Options struct {
	Mapper *mapping.Mapper
	// NoPositions suppresses line/column information (used for objects that
	// did not come from a file).
	NoPositions bool
}

// Run lints all files.
func Run(files []*manifest.File, opts Options) []Finding {
	if opts.Mapper == nil {
		opts.Mapper = mapping.Default()
	}
	l := &linter{opts: opts}
	for _, f := range files {
		l.file(f)
	}
	sort.SliceStable(l.out, func(i, j int) bool {
		a, b := l.out[i], l.out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return l.out
}

type linter struct {
	opts Options
	out  []Finding
}

type ctx struct {
	file *manifest.File
	seg  *manifest.Segment
	obj  *manifest.Object
	ps   *manifest.PodSpec
}

func (l *linter) add(c ctx, r Rule, n *yaml.Node, f Finding, format string, args ...any) {
	f.RuleID, f.Rule, f.Severity = r.ID, r.Name, r.Severity
	f.File = c.file.Path
	f.Message = fmt.Sprintf(format, args...)
	if !l.opts.NoPositions && c.seg != nil {
		f.Line = c.seg.Line(n)
		if n != nil {
			f.Column = n.Column
		}
	}
	if c.obj != nil {
		f.Kind, f.Namespace, f.Name = c.obj.Kind, c.obj.Namespace, c.obj.Name
		if f.Name == "" && c.obj.GenerateName != "" {
			f.Name = c.obj.GenerateName + "*"
		}
	}
	if c.ps != nil {
		f.Path = c.ps.Path
	}
	l.out = append(l.out, f)
}

func (l *linter) file(f *manifest.File) {
	helmReported := false
	for _, seg := range f.Segments {
		c := ctx{file: f, seg: seg}
		switch {
		case seg.HelmTemplate:
			if !helmReported {
				helmReported = true
				l.add(c, RuleHelm, nil, Finding{}, "unrendered Helm template; render it first (helm template . | dra-migrate lint -)")
			}
			continue
		case seg.Err != nil:
			l.add(c, RuleParse, nil, Finding{}, "cannot parse YAML: %v", seg.Err)
			continue
		}
		for _, obj := range seg.Objects {
			c.obj = obj
			for _, ps := range obj.PodSpecs {
				c.ps = ps
				l.podSpec(c)
			}
		}
	}
}

func (l *linter) podSpec(c ctx) {
	m := l.opts.Mapper
	gpuWorkload := false
	nvidia := false
	for _, ctr := range c.ps.Containers {
		for _, e := range ctr.Entries(m.Candidate) {
			res, mapped := m.Resolve(e.Resource)
			q := e.Primary()
			side := "limits"
			if e.Limit == nil {
				side = "requests"
			}
			base := Finding{Container: ctr.Name, Resource: e.Resource}
			if !mapped {
				l.add(c, RuleUnmapped, q.Key, base,
					"container %q: no DRA mapping known for %s; add it to a --mapping-file if it is a device", ctr.Name, e.Resource)
				continue
			}
			count, valid := e.Count()
			if !valid {
				l.add(c, RuleBadQuantity, q.Val, base,
					"container %q: %s has quantity %q, which is not a whole number of devices", ctr.Name, e.Resource, nodeValue(q.Val))
				continue
			}
			base.Count = &count
			if count == 0 && (e.Request == nil || e.Request.Count == 0) {
				l.add(c, RuleZero, q.Key, base, "container %q: %s: 0 allocates nothing", ctr.Name, e.Resource)
				continue
			}
			gpuWorkload = true
			if strings.HasPrefix(e.Resource, mapping.NvidiaResourcePrefix) {
				nvidia = true
			}
			fix := base
			fix.Fixable = true
			sel := ""
			if len(res.Selectors) > 0 {
				sel = " with a CEL selector"
			}
			l.add(c, RuleLegacyRequest, q.Key, fix,
				"container %q: %s: %d (%s) is a device-plugin request; replace it with a DRA claim on DeviceClass %s%s",
				ctr.Name, e.Resource, count, side, res.DeviceClass, sel)

			switch {
			case e.Limit == nil:
				l.add(c, RuleMismatch, e.Request.Key, base,
					"container %q: %s is set in requests but not in limits; the API server rejects this", ctr.Name, e.Resource)
			case e.Request != nil && (!e.Request.Valid || e.Request.Count != e.Limit.Count):
				l.add(c, RuleMismatch, e.Request.Key, base,
					"container %q: %s request (%s) differs from limit (%s); extended resources require them to be equal",
					ctr.Name, e.Resource, nodeValue(e.Request.Val), nodeValue(e.Limit.Val))
			}
			if res.Shared {
				l.add(c, RuleShared, q.Key, base,
					"container %q: %s is an oversubscribed resource; a DRA claim template gives each pod an exclusive device unless pods share one ResourceClaim (--claim-mode claim)",
					ctr.Name, e.Resource)
			}
			if ctr.Init && !ctr.Sidecar {
				l.add(c, RuleInit, q.Key, base,
					"init container %q requests %s; DRA claims are held for the whole pod lifetime, review how the claim is shared with app containers",
					ctr.Name, e.Resource)
			}
		}
	}
	if !gpuWorkload {
		return
	}
	if !manifest.IsBuiltinWorkload(c.obj.APIVersion, c.obj.Kind) {
		l.add(c, RuleCustomKind, c.ps.Node, Finding{},
			"pod template found at %s of custom resource %s; verify that its CRD schema accepts resourceClaims", pathOrRoot(c.ps.Path), c.obj.Kind)
	}
	if !nvidia {
		return
	}
	l.nodeSelector(c)
	l.affinity(c)
	l.runtimeClass(c)
	l.tolerations(c)
	l.env(c)
}

func pathOrRoot(p string) string {
	if p == "" {
		return "the document root"
	}
	return p
}

func nodeValue(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	if n.Kind != yaml.ScalarNode {
		return "<non-scalar>"
	}
	return n.Value
}

func (l *linter) nodeSelector(c ctx) {
	ns := yamlutil.Value(c.ps.Node, "nodeSelector")
	if !yamlutil.IsMap(ns) {
		return
	}
	for i := 0; i+1 < len(ns.Content); i += 2 {
		k, v := ns.Content[i], yamlutil.Resolve(ns.Content[i+1])
		if !strings.HasPrefix(k.Value, mapping.NvidiaResourcePrefix) {
			continue
		}
		if mapping.DeviceLabel(k.Value) {
			if expr, ok := mapping.SelectorForNodeLabel(k.Value, nodeValue(v)); ok {
				l.add(c, RuleNodeSelector, k, Finding{},
					"nodeSelector %s=%s describes the device and could become a CEL selector (%s); see convert --map-node-selectors",
					k.Value, nodeValue(v), expr)
				continue
			}
		}
		l.add(c, RuleNodeSelector, k, Finding{},
			"nodeSelector %s=%s relies on a GPU Feature Discovery label; it keeps working only while GFD labels the nodes", k.Value, nodeValue(v))
	}
}

func (l *linter) affinity(c ctx) {
	na := yamlutil.Path(c.ps.Node, "affinity", "nodeAffinity")
	if na == nil {
		return
	}
	seen := map[string]bool{}
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		n = yamlutil.Resolve(n)
		if n == nil || depth > 32 {
			return
		}
		if n.Kind == yaml.MappingNode {
			if _, v := yamlutil.Get(n, "key"); v != nil && v.Kind == yaml.ScalarNode &&
				strings.HasPrefix(v.Value, mapping.NvidiaResourcePrefix) && !seen[v.Value] {
				seen[v.Value] = true
				l.add(c, RuleNodeAffinity, v, Finding{},
					"node affinity on %s is not rewritten; consider a CEL selector on the device request", v.Value)
			}
		}
		for _, ch := range n.Content {
			walk(ch, depth+1)
		}
	}
	walk(na, 0)
}

func (l *linter) runtimeClass(c ctx) {
	k, v := yamlutil.Get(c.ps.Node, "runtimeClassName")
	if v == nil || !strings.Contains(strings.ToLower(v.Value), "nvidia") {
		return
	}
	l.add(c, RuleRuntimeClass, k, Finding{},
		"runtimeClassName %q may be unnecessary with the NVIDIA DRA driver, which injects devices through CDI; verify on your nodes", v.Value)
}

func (l *linter) tolerations(c ctx) {
	ts := yamlutil.Value(c.ps.Node, "tolerations")
	if yamlutil.IsSeq(ts) {
		for _, t := range ts.Content {
			t = yamlutil.Resolve(t)
			key := yamlutil.String(t, "key")
			op := yamlutil.String(t, "operator")
			if key == "nvidia.com/gpu" || (key == "" && op == "Exists") {
				return
			}
		}
	}
	l.add(c, RuleToleration, c.ps.Node, Finding{},
		"no toleration for taint nvidia.com/gpu; if GPU nodes are tainted and you rely on the ExtendedResourceToleration admission plugin, add the toleration explicitly")
}

func (l *linter) env(c ctx) {
	for _, ctr := range c.ps.Containers {
		env := yamlutil.Value(ctr.Node, "env")
		if !yamlutil.IsSeq(env) {
			continue
		}
		for _, e := range env.Content {
			e = yamlutil.Resolve(e)
			k, v := yamlutil.Get(e, "name")
			if v != nil && v.Value == "NVIDIA_VISIBLE_DEVICES" {
				l.add(c, RuleVisibleDevices, k, Finding{Container: ctr.Name},
					"container %q sets NVIDIA_VISIBLE_DEVICES, which overrides device allocation; remove it when using DRA", ctr.Name)
			}
		}
	}
}

// Summary counts findings by severity.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
	Fixable  int `json:"fixable"`
}

// Summarize counts findings.
func Summarize(fs []Finding) Summary {
	var s Summary
	for _, f := range fs {
		switch f.Severity {
		case Error:
			s.Errors++
		case Warning:
			s.Warnings++
		case Info:
			s.Infos++
		}
		if f.Fixable {
			s.Fixable++
		}
	}
	return s
}

// Failed reports whether any finding is at or above the threshold. An empty
// threshold never fails.
func Failed(fs []Finding, threshold Severity) bool {
	if threshold == "" {
		return false
	}
	for _, f := range fs {
		if f.Severity.Rank() >= threshold.Rank() {
			return true
		}
	}
	return false
}
