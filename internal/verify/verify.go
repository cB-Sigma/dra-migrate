// Package verify checks manifests against the real Kubernetes API types. It
// is used by the test suite to guarantee that everything dra-migrate
// generates is schema-valid: every document is decoded strictly (unknown
// and duplicate fields are errors) into the Go type from k8s.io/api, claim
// references are resolved, and CEL selectors are compiled with the DRA CEL
// environment of the scheduler.
package verify

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"sigs.k8s.io/yaml"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// PodClaim is a pod-level resource claim reference.
type PodClaim struct {
	Workload  string
	Namespace string
	Name      string
	Template  string
	Claim     string
}

// Request is a device request found in a claim or claim template.
type Request struct {
	Object      string // Kind/namespace/name
	Name        string
	DeviceClass string
	Count       int64
	Selectors   []string
	HasConfig   bool
}

// Summary describes what was found in a set of manifests.
type Summary struct {
	Objects   int
	Templates map[string]bool // namespace/name
	Claims    map[string]bool // namespace/name
	PodClaims []PodClaim
	Requests  []Request
	// LegacyResources lists extended resources with the nvidia.com/ prefix
	// still present in container limits or requests, with their quantity.
	LegacyResources map[string]int64
}

// Manifests strictly validates multi-document YAML.
func Manifests(data []byte) (*Summary, error) {
	f := manifest.Parse("verify.yaml", "verify.yaml", data)
	if f.HelmTemplate {
		return nil, fmt.Errorf("input is a Helm template")
	}
	sum := &Summary{Templates: map[string]bool{}, Claims: map[string]bool{}, LegacyResources: map[string]int64{}}
	for _, seg := range f.Segments {
		if seg.Err != nil {
			return nil, fmt.Errorf("line %d: %w", seg.StartLine, seg.Err)
		}
		for _, obj := range seg.Objects {
			sum.Objects++
			if err := object(sum, obj); err != nil {
				return nil, fmt.Errorf("%s (line %d): %w", obj.DisplayName(), seg.Line(obj.Root), err)
			}
		}
	}
	for _, pc := range sum.PodClaims {
		if (pc.Template == "") == (pc.Claim == "") {
			return nil, fmt.Errorf("%s: pod claim %q must set exactly one of resourceClaimTemplateName and resourceClaimName", pc.Workload, pc.Name)
		}
	}
	return sum, nil
}

func strict(n any, into any) error {
	data, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	return yaml.UnmarshalStrict(data, into)
}

func decodeNode(obj *manifest.Object, into any) error {
	text, err := yamlutil.Encode(obj.Root, yamlutil.DefaultStyle)
	if err != nil {
		return err
	}
	if err := yaml.UnmarshalStrict([]byte(text), into); err != nil {
		return fmt.Errorf("strict decoding into %T: %w", into, err)
	}
	return nil
}

func object(sum *Summary, obj *manifest.Object) error {
	if obj.Name != "" {
		if errs := validation.IsDNS1123Subdomain(obj.Name); len(errs) > 0 {
			return fmt.Errorf("invalid name %q: %s", obj.Name, strings.Join(errs, "; "))
		}
	}
	id := obj.Kind + "/" + obj.Namespace + "/" + obj.Name
	nsName := obj.Namespace + "/" + obj.Name
	var pods []*corev1.PodSpec
	switch obj.APIVersion + "/" + obj.Kind {
	case "v1/Pod":
		var o corev1.Pod
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec)
	case "v1/PodTemplate":
		var o corev1.PodTemplate
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Template.Spec)
	case "v1/ReplicationController":
		var o corev1.ReplicationController
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		if o.Spec.Template != nil {
			pods = append(pods, &o.Spec.Template.Spec)
		}
	case "v1/ConfigMap":
		return decodeNode(obj, &corev1.ConfigMap{})
	case "v1/Service":
		return decodeNode(obj, &corev1.Service{})
	case "v1/ServiceAccount":
		return decodeNode(obj, &corev1.ServiceAccount{})
	case "v1/Namespace":
		return decodeNode(obj, &corev1.Namespace{})
	case "apps/v1/Deployment":
		var o appsv1.Deployment
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.Template.Spec)
	case "apps/v1/StatefulSet":
		var o appsv1.StatefulSet
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.Template.Spec)
	case "apps/v1/DaemonSet":
		var o appsv1.DaemonSet
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.Template.Spec)
	case "apps/v1/ReplicaSet":
		var o appsv1.ReplicaSet
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.Template.Spec)
	case "batch/v1/Job":
		var o batchv1.Job
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.Template.Spec)
	case "batch/v1/CronJob":
		var o batchv1.CronJob
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		pods = append(pods, &o.Spec.JobTemplate.Spec.Template.Spec)
	case "resource.k8s.io/v1/ResourceClaimTemplate":
		var o resourcev1.ResourceClaimTemplate
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Templates[nsName] = true
		return claimSpecV1(sum, id, &o.Spec.Spec)
	case "resource.k8s.io/v1/ResourceClaim":
		var o resourcev1.ResourceClaim
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Claims[nsName] = true
		return claimSpecV1(sum, id, &o.Spec)
	case "resource.k8s.io/v1beta2/ResourceClaimTemplate":
		var o resourcev1beta2.ResourceClaimTemplate
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Templates[nsName] = true
		return claimSpecViaV1(sum, id, &o.Spec.Spec)
	case "resource.k8s.io/v1beta2/ResourceClaim":
		var o resourcev1beta2.ResourceClaim
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Claims[nsName] = true
		return claimSpecViaV1(sum, id, &o.Spec)
	case "resource.k8s.io/v1beta1/ResourceClaimTemplate":
		var o resourcev1beta1.ResourceClaimTemplate
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Templates[nsName] = true
		return claimSpecV1beta1(sum, id, &o.Spec.Spec)
	case "resource.k8s.io/v1beta1/ResourceClaim":
		var o resourcev1beta1.ResourceClaim
		if err := decodeNode(obj, &o); err != nil {
			return err
		}
		sum.Claims[nsName] = true
		return claimSpecV1beta1(sum, id, &o.Spec)
	default:
		// Unknown kind (custom resource): validate the embedded pod specs.
		for _, ps := range obj.PodSpecs {
			var spec corev1.PodSpec
			var generic any
			if err := ps.Node.Decode(&generic); err != nil {
				return err
			}
			if err := strict(generic, &spec); err != nil {
				return fmt.Errorf("pod spec at %s: %w", ps.Path, err)
			}
			pods = append(pods, &spec)
		}
	}
	for _, ps := range pods {
		if err := podSpec(sum, obj, ps); err != nil {
			return err
		}
	}
	return nil
}

func podSpec(sum *Summary, obj *manifest.Object, ps *corev1.PodSpec) error {
	declared := map[string]bool{}
	for _, rc := range ps.ResourceClaims {
		if errs := validation.IsDNS1123Label(rc.Name); len(errs) > 0 {
			return fmt.Errorf("resourceClaims name %q: %s", rc.Name, strings.Join(errs, "; "))
		}
		if declared[rc.Name] {
			return fmt.Errorf("duplicate resourceClaims name %q", rc.Name)
		}
		declared[rc.Name] = true
		pc := PodClaim{Workload: obj.DisplayName(), Namespace: obj.Namespace, Name: rc.Name}
		if rc.ResourceClaimTemplateName != nil {
			pc.Template = *rc.ResourceClaimTemplateName
		}
		if rc.ResourceClaimName != nil {
			pc.Claim = *rc.ResourceClaimName
		}
		sum.PodClaims = append(sum.PodClaims, pc)
	}
	all := append(append([]corev1.Container{}, ps.InitContainers...), ps.Containers...)
	for _, c := range all {
		seen := map[string]bool{}
		for _, cl := range c.Resources.Claims {
			if !declared[cl.Name] {
				return fmt.Errorf("container %q references claim %q, which is not in spec.resourceClaims", c.Name, cl.Name)
			}
			if seen[cl.Name+"/"+cl.Request] {
				return fmt.Errorf("container %q references claim %q twice", c.Name, cl.Name)
			}
			seen[cl.Name+"/"+cl.Request] = true
		}
		for _, list := range []corev1.ResourceList{c.Resources.Limits, c.Resources.Requests} {
			for name, q := range list {
				if strings.HasPrefix(string(name), "nvidia.com/") {
					sum.LegacyResources[string(name)] += q.Value()
				}
			}
		}
	}
	return nil
}

func claimSpecViaV1(sum *Summary, id string, spec *resourcev1beta2.ResourceClaimSpec) error {
	// v1beta2 and v1 are structurally identical; round-trip through YAML
	// with strict decoding to reuse the v1 checks.
	var v1spec resourcev1.ResourceClaimSpec
	if err := strict(spec, &v1spec); err != nil {
		return fmt.Errorf("v1beta2 spec is not convertible to v1: %w", err)
	}
	return claimSpecV1(sum, id, &v1spec)
}

func claimSpecV1(sum *Summary, id string, spec *resourcev1.ResourceClaimSpec) error {
	names := map[string]bool{}
	if len(spec.Devices.Requests) == 0 {
		return fmt.Errorf("no device requests")
	}
	for _, r := range spec.Devices.Requests {
		if r.Exactly == nil {
			return fmt.Errorf("request %q: missing 'exactly'", r.Name)
		}
		var sels []string
		for _, s := range r.Exactly.Selectors {
			if s.CEL == nil {
				return fmt.Errorf("request %q: selector without cel", r.Name)
			}
			sels = append(sels, s.CEL.Expression)
		}
		req := Request{Object: id, Name: r.Name, DeviceClass: r.Exactly.DeviceClassName, Count: r.Exactly.Count, Selectors: sels}
		if err := request(&req, string(r.Exactly.AllocationMode)); err != nil {
			return err
		}
		names[r.Name] = true
		sum.Requests = append(sum.Requests, req)
	}
	for _, c := range spec.Devices.Config {
		if c.Opaque == nil {
			return fmt.Errorf("config without opaque parameters")
		}
		if err := opaque(c.Opaque.Driver, c.Opaque.Parameters.Raw, c.Requests, names); err != nil {
			return err
		}
		markConfig(sum, id, c.Requests)
	}
	return nil
}

func claimSpecV1beta1(sum *Summary, id string, spec *resourcev1beta1.ResourceClaimSpec) error {
	names := map[string]bool{}
	if len(spec.Devices.Requests) == 0 {
		return fmt.Errorf("no device requests")
	}
	for _, r := range spec.Devices.Requests {
		var sels []string
		for _, s := range r.Selectors {
			if s.CEL == nil {
				return fmt.Errorf("request %q: selector without cel", r.Name)
			}
			sels = append(sels, s.CEL.Expression)
		}
		req := Request{Object: id, Name: r.Name, DeviceClass: r.DeviceClassName, Count: r.Count, Selectors: sels}
		if err := request(&req, string(r.AllocationMode)); err != nil {
			return err
		}
		names[r.Name] = true
		sum.Requests = append(sum.Requests, req)
	}
	for _, c := range spec.Devices.Config {
		if c.Opaque == nil {
			return fmt.Errorf("config without opaque parameters")
		}
		if err := opaque(c.Opaque.Driver, c.Opaque.Parameters.Raw, c.Requests, names); err != nil {
			return err
		}
		markConfig(sum, id, c.Requests)
	}
	return nil
}

func markConfig(sum *Summary, id string, requests []string) {
	for i := range sum.Requests {
		if sum.Requests[i].Object != id {
			continue
		}
		for _, n := range requests {
			if sum.Requests[i].Name == n {
				sum.Requests[i].HasConfig = true
			}
		}
	}
}

func opaque(driver string, raw []byte, requests []string, names map[string]bool) error {
	if errs := validation.IsDNS1123Subdomain(driver); len(errs) > 0 {
		return fmt.Errorf("opaque config driver %q: %s", driver, strings.Join(errs, "; "))
	}
	var params map[string]any
	if err := yaml.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("opaque parameters: %w", err)
	}
	if params["apiVersion"] == nil || params["kind"] == nil {
		return fmt.Errorf("opaque parameters must be an object with apiVersion and kind")
	}
	for _, r := range requests {
		if !names[r] {
			return fmt.Errorf("config references unknown request %q", r)
		}
	}
	return nil
}

// request applies the API server's defaulting and the validation rules
// that do not need cluster state.
func request(r *Request, mode string) error {
	if errs := validation.IsDNS1123Label(r.Name); len(errs) > 0 {
		return fmt.Errorf("request name %q: %s", r.Name, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(r.DeviceClass); len(errs) > 0 {
		return fmt.Errorf("request %q: deviceClassName %q: %s", r.Name, r.DeviceClass, strings.Join(errs, "; "))
	}
	switch mode {
	case "":
		if r.Count != 0 {
			return fmt.Errorf("request %q: count set without allocationMode", r.Name)
		}
		r.Count = 1
	case "ExactCount":
		if r.Count <= 0 {
			return fmt.Errorf("request %q: count must be positive", r.Name)
		}
	case "All":
		if r.Count != 0 {
			return fmt.Errorf("request %q: count must not be set with allocationMode All", r.Name)
		}
	default:
		return fmt.Errorf("request %q: unknown allocationMode %q", r.Name, mode)
	}
	for _, expr := range r.Selectors {
		if err := CompileCEL(expr); err != nil {
			return fmt.Errorf("request %q: %w", r.Name, err)
		}
	}
	return nil
}

var celCompiler = dracel.GetCompiler(dracel.Features{})

// CompileCEL compiles a device selector with the DRA CEL environment.
func CompileCEL(expr string) error {
	res := celCompiler.CompileCELExpression(expr, dracel.Options{})
	if res.Error != nil {
		return fmt.Errorf("CEL expression %q does not compile: %s", expr, res.Error.Detail)
	}
	return nil
}

// Device is a simplified device for evaluating selectors in tests.
type Device struct {
	Driver     string
	Attributes map[string]string // string attributes in the driver's domain
	Memory     string            // capacity "memory" in the driver's domain
}

// MatchCEL evaluates a selector against a device.
func MatchCEL(expr string, d Device) (bool, error) {
	res := celCompiler.CompileCELExpression(expr, dracel.Options{})
	if res.Error != nil {
		return false, fmt.Errorf("CEL expression %q does not compile: %s", expr, res.Error.Detail)
	}
	dev := dracel.Device{
		Driver:     d.Driver,
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{},
		Capacity:   map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{},
	}
	for k, v := range d.Attributes {
		dev.Attributes[resourcev1.QualifiedName(k)] = resourcev1.DeviceAttribute{StringValue: &v}
	}
	if d.Memory != "" {
		dev.Capacity["memory"] = resourcev1.DeviceCapacity{Value: resource.MustParse(d.Memory)}
	}
	ok, _, err := res.DeviceMatches(context.Background(), dev)
	return ok, err
}
