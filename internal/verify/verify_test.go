package verify

import (
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/mapping"
)

// Sample devices, modelled on the ResourceSlice reference of the NVIDIA DRA
// driver documentation.
var (
	a100 = Device{Driver: "gpu.nvidia.com", Memory: "40Gi", Attributes: map[string]string{
		"type": "gpu", "productName": "NVIDIA A100-SXM4-40GB", "architecture": "Ampere", "brand": "Nvidia"}}
	t4 = Device{Driver: "gpu.nvidia.com", Memory: "15360Mi", Attributes: map[string]string{
		"type": "gpu", "productName": "Tesla T4", "architecture": "Turing", "brand": "Tesla"}}
	mig = Device{Driver: "gpu.nvidia.com", Memory: "9728Mi", Attributes: map[string]string{
		"type": "mig", "productName": "NVIDIA A100-SXM4-40GB", "architecture": "Ampere", "profile": "1g.10gb"}}
)

// TestGeneratedSelectorsEvaluate compiles the selectors dra-migrate
// generates with the scheduler's CEL environment and evaluates them against
// sample devices.
func TestGeneratedSelectorsEvaluate(t *testing.T) {
	m := mapping.Default()
	migRule, _ := m.Resolve("nvidia.com/mig-1g.10gb")
	if len(migRule.Selectors) != 1 {
		t.Fatalf("MIG rule selectors: %v", migRule.Selectors)
	}
	product := func(v string) string {
		expr, ok := mapping.SelectorForNodeLabel(mapping.LabelGPUProduct, v)
		if !ok {
			t.Fatalf("no selector for product %q", v)
		}
		return expr
	}
	memory, _ := mapping.SelectorForNodeLabel(mapping.LabelGPUMemory, "40960")
	family, _ := mapping.SelectorForNodeLabel(mapping.LabelGPUFamily, "ampere")

	tests := []struct {
		name string
		expr string
		dev  Device
		want bool
	}{
		{"mig profile matches", migRule.Selectors[0], mig, true},
		{"mig profile rejects other profile", strings.Replace(migRule.Selectors[0], "1g.10gb", "3g.20gb", 1), mig, false},
		{"GFD product label", product("A100-SXM4-40GB"), a100, true},
		{"GFD product label with vendor prefix", product("NVIDIA-A100-SXM4-40GB"), a100, true},
		{"GFD product label of a time-sliced node", product("A100-SXM4-40GB-SHARED"), a100, true},
		{"GFD product label with space in product", product("Tesla-T4"), t4, true},
		{"GFD product label rejects other model", product("A100-SXM4-40GB"), t4, false},
		{"memory label matches", memory, a100, true},
		{"memory label rejects smaller GPU", memory, t4, false},
		{"family label matches", family, a100, true},
		{"family label rejects other family", family, t4, false},
	}
	for _, tt := range tests {
		got, err := MatchCEL(tt.expr, tt.dev)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: %s = %v, want %v", tt.name, tt.expr, got, tt.want)
		}
	}
}

func TestCompileCELRejectsGarbage(t *testing.T) {
	if err := CompileCEL("device.attributes['x'].y =="); err == nil {
		t.Error("expected a compile error")
	}
	if err := CompileCEL("device.nonexistent == 1"); err == nil {
		t.Error("expected a type-check error")
	}
}

func TestManifestsRejectsInvalid(t *testing.T) {
	bad := map[string]string{
		"unknown field in claim template": `apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata: {name: x}
spec:
  spec:
    devices:
      requests:
      - name: gpu
        deviceClassName: gpu.nvidia.com
`,
		"exactly used with v1beta1": `apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaimTemplate
metadata: {name: x}
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.nvidia.com
`,
		"count without allocation mode": `apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata: {name: x}
spec:
  devices:
    requests:
    - name: gpu
      exactly:
        deviceClassName: gpu.nvidia.com
        count: 2
`,
		"broken CEL": `apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata: {name: x}
spec:
  devices:
    requests:
    - name: gpu
      exactly:
        deviceClassName: gpu.nvidia.com
        selectors:
        - cel:
            expression: "device.attributes['gpu.nvidia.com'].profile = '1g'"
`,
		"container claim not declared": `apiVersion: v1
kind: Pod
metadata: {name: x}
spec:
  containers:
  - name: c
    image: i
    resources:
      claims:
      - name: gpu
`,
		"pod claim with both sources": `apiVersion: v1
kind: Pod
metadata: {name: x}
spec:
  containers:
  - name: c
    image: i
  resourceClaims:
  - name: gpu
    resourceClaimName: a
    resourceClaimTemplateName: b
`,
		"unknown pod field": `apiVersion: apps/v1
kind: Deployment
metadata: {name: x}
spec:
  template:
    spec:
      containers:
      - name: c
        image: i
      resourceClaim: []
`,
		"unknown field in custom resource pod spec": `apiVersion: example.com/v1
kind: Thing
metadata: {name: x}
spec:
  template:
    spec:
      containers:
      - name: c
        image: i
        resources:
          claim: []
`,
	}
	for name, doc := range bad {
		if _, err := Manifests([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	good := `apiVersion: resource.k8s.io/v1beta2
kind: ResourceClaimTemplate
metadata: {name: x, namespace: ns}
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.nvidia.com
          allocationMode: ExactCount
          count: 2
`
	sum, err := Manifests([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Templates["ns/x"] || len(sum.Requests) != 1 || sum.Requests[0].Count != 2 {
		t.Errorf("summary: %+v", sum)
	}
}
