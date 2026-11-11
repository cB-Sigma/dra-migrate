package manifest

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// Object is a Kubernetes object found in a document.
type Object struct {
	APIVersion   string
	Kind         string
	Name         string
	GenerateName string
	Namespace    string
	Root         *yaml.Node
	// InList is set for objects that are items of a List.
	InList bool
	// PodSpecs are all pod specifications found inside the object.
	PodSpecs []*PodSpec
}

// DisplayName returns "Kind/name" (or "Kind/namespace/name").
func (o *Object) DisplayName() string {
	name := o.Name
	if name == "" && o.GenerateName != "" {
		name = o.GenerateName + "*"
	}
	if name == "" {
		name = "<unnamed>"
	}
	if o.Namespace != "" {
		return o.Kind + "/" + o.Namespace + "/" + name
	}
	return o.Kind + "/" + name
}

// BaseName returns a name usable as a prefix for generated objects.
func (o *Object) BaseName() string {
	if o.Name != "" {
		return o.Name
	}
	if n := strings.TrimRight(o.GenerateName, "-."); n != "" {
		return n
	}
	return strings.ToLower(o.Kind)
}

// builtinPodSpecPaths lists where the pod spec lives in core workload kinds.
// Other kinds are searched generically.
var builtinPodSpecPaths = map[string]string{
	"Pod":                   "spec",
	"PodTemplate":           "template.spec",
	"Deployment":            "spec.template.spec",
	"StatefulSet":           "spec.template.spec",
	"DaemonSet":             "spec.template.spec",
	"ReplicaSet":            "spec.template.spec",
	"ReplicationController": "spec.template.spec",
	"Job":                   "spec.template.spec",
	"CronJob":               "spec.jobTemplate.spec.template.spec",
}

// IsBuiltinWorkload reports whether kind is a core workload kind whose pod
// template schema is known to accept resource claims.
func IsBuiltinWorkload(apiVersion, kind string) bool {
	if _, ok := builtinPodSpecPaths[kind]; !ok {
		return false
	}
	group := ""
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		group = apiVersion[:i]
	}
	switch group {
	case "", "apps", "batch":
		return true
	}
	return false
}

// PodSpec is a pod specification inside an object.
type PodSpec struct {
	// Node is the mapping node of the pod spec.
	Node *yaml.Node
	// Path is the dotted path from the object root, e.g. "spec.template.spec".
	Path string
	// Containers are init containers followed by regular containers.
	Containers []*Container
}

// Container is a container inside a pod spec.
type Container struct {
	Name string
	Node *yaml.Node
	// Init is set for init containers, Sidecar for init containers with
	// restartPolicy: Always (which run for the lifetime of the pod).
	Init    bool
	Sidecar bool
	// Field is "containers" or "initContainers".
	Field string
	Index int
}

func objectsIn(root *yaml.Node) []*Object {
	kind := yamlutil.String(root, "kind")
	apiVersion := yamlutil.String(root, "apiVersion")
	if kind == "" || apiVersion == "" {
		return nil
	}
	if strings.HasSuffix(kind, "List") && yamlutil.IsSeq(yamlutil.Value(root, "items")) {
		var out []*Object
		for _, item := range yamlutil.Value(root, "items").Content {
			item = yamlutil.Resolve(item)
			if !yamlutil.IsMap(item) {
				continue
			}
			for _, o := range objectsIn(item) {
				o.InList = true
				out = append(out, o)
			}
		}
		return out
	}
	o := &Object{
		APIVersion:   apiVersion,
		Kind:         kind,
		Name:         yamlutil.String(root, "metadata", "name"),
		GenerateName: yamlutil.String(root, "metadata", "generateName"),
		Namespace:    yamlutil.String(root, "metadata", "namespace"),
		Root:         root,
	}
	findPodSpecs(root, "", 0, &o.PodSpecs)
	return []*Object{o}
}

const maxDepth = 64

// findPodSpecs searches the tree for mappings that look like a PodSpec: a
// mapping with a "containers" key holding a sequence of mappings that each
// have a name. This finds pod templates in core workloads and in custom
// resources that embed a PodTemplateSpec (Argo Rollouts, KubeRay, Kubeflow
// training jobs, JobSet, Volcano, Knative, ...).
func findPodSpecs(n *yaml.Node, path string, depth int, out *[]*PodSpec) {
	n = yamlutil.Resolve(n)
	if n == nil || depth > maxDepth {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		if ps := asPodSpec(n, path); ps != nil {
			*out = append(*out, ps)
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if depth == 0 && (key == "status" || key == "metadata") {
				continue
			}
			p := key
			if path != "" {
				p = path + "." + key
			}
			findPodSpecs(n.Content[i+1], p, depth+1, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			findPodSpecs(c, path+"["+strconv.Itoa(i)+"]", depth+1, out)
		}
	}
}

func asPodSpec(n *yaml.Node, path string) *PodSpec {
	cs := yamlutil.Value(n, "containers")
	if !yamlutil.IsSeq(cs) || len(cs.Content) == 0 {
		return nil
	}
	ps := &PodSpec{Node: n, Path: path}
	for _, field := range []string{"initContainers", "containers"} {
		seq := yamlutil.Value(n, field)
		if !yamlutil.IsSeq(seq) {
			continue
		}
		for i, c := range seq.Content {
			c = yamlutil.Resolve(c)
			if !yamlutil.IsMap(c) {
				if field == "containers" {
					return nil
				}
				continue
			}
			name := yamlutil.String(c, "name")
			if name == "" && field == "containers" {
				return nil
			}
			ctr := &Container{Name: name, Node: c, Field: field, Index: i}
			if field == "initContainers" {
				ctr.Init = true
				if yamlutil.String(c, "restartPolicy") == "Always" {
					ctr.Sidecar = true
				}
			}
			ps.Containers = append(ps.Containers, ctr)
		}
	}
	return ps
}

// Quantity is one side (limits or requests) of an extended resource entry.
type Quantity struct {
	Key   *yaml.Node
	Val   *yaml.Node
	Map   *yaml.Node // the limits or requests mapping
	Count int64
	Valid bool // the value parsed as a whole, non-negative quantity
}

// ResourceEntry is an extended resource named in a container's limits
// and/or requests.
type ResourceEntry struct {
	Resource string
	Limit    *Quantity
	Request  *Quantity
}

// Count returns the effective device count: the limit, or the request when
// no limit is set.
func (e *ResourceEntry) Count() (int64, bool) {
	q := e.Primary()
	return q.Count, q.Valid
}

// Primary returns the limit entry if present, the request entry otherwise.
func (e *ResourceEntry) Primary() *Quantity {
	if e.Limit != nil {
		return e.Limit
	}
	return e.Request
}

// Resources returns the container's resources mapping, or nil.
func (c *Container) Resources() *yaml.Node {
	r := yamlutil.Value(c.Node, "resources")
	if !yamlutil.IsMap(r) {
		return nil
	}
	return r
}

// Entries returns the resources for which match returns true, in order of
// first appearance (limits first).
func (c *Container) Entries(match func(name string) bool) []*ResourceEntry {
	res := c.Resources()
	if res == nil {
		return nil
	}
	var out []*ResourceEntry
	byName := map[string]*ResourceEntry{}
	for _, side := range []string{"limits", "requests"} {
		m := yamlutil.Value(res, side)
		if !yamlutil.IsMap(m) {
			continue
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i], yamlutil.Resolve(m.Content[i+1])
			if !match(k.Value) {
				continue
			}
			e := byName[k.Value]
			if e == nil {
				e = &ResourceEntry{Resource: k.Value}
				byName[k.Value] = e
				out = append(out, e)
			}
			q := &Quantity{Key: k, Val: v, Map: m}
			if v != nil && v.Kind == yaml.ScalarNode {
				if parsed, err := resource.ParseQuantity(strings.TrimSpace(v.Value)); err == nil {
					if n, ok := parsed.AsInt64(); ok && n >= 0 {
						q.Count, q.Valid = n, true
					}
				}
			}
			if side == "limits" {
				e.Limit = q
			} else {
				e.Request = q
			}
		}
	}
	return out
}

// ClaimNames returns the names in the pod spec's resourceClaims list.
func (p *PodSpec) ClaimNames() []string {
	seq := yamlutil.Value(p.Node, "resourceClaims")
	if !yamlutil.IsSeq(seq) {
		return nil
	}
	var names []string
	for _, c := range seq.Content {
		if n := yamlutil.String(yamlutil.Resolve(c), "name"); n != "" {
			names = append(names, n)
		}
	}
	return names
}
