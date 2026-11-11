package lint

import (
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/mapping"
)

func lintDoc(t *testing.T, doc string) []Finding {
	t.Helper()
	return Run([]*manifest.File{manifest.Parse("test.yaml", "test.yaml", []byte(doc))}, Options{})
}

func ids(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.RuleID)
	}
	return strings.Join(out, " ")
}

func find(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.RuleID == id {
			return f
		}
	}
	t.Fatalf("no %s finding in: %s", id, ids(fs))
	return Finding{}
}

const fullPod = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: trainer
  namespace: ml
spec:
  template:
    spec:
      runtimeClassName: nvidia
      nodeSelector:
        nvidia.com/gpu.product: A100-SXM4-40GB
        nvidia.com/gpu.present: "true"
        disktype: ssd
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: nvidia.com/gpu.count
                operator: Gt
                values: ["3"]
      initContainers:
      - name: check
        image: x
        resources:
          limits:
            nvidia.com/gpu: 1
      containers:
      - name: main
        image: x
        env:
        - name: NVIDIA_VISIBLE_DEVICES
          value: all
        resources:
          limits:
            nvidia.com/gpu: 4
            nvidia.com/gpu.shared: 2
            nvidia.com/mig-1g.5gb: 1
            nvidia.com/rdma_shared_device_a: 1
          requests:
            nvidia.com/gpu: 2
      - name: second
        image: x
        resources:
          requests:
            nvidia.com/gpu: 1
          limits:
            nvidia.com/mig-2g.10gb: 0
            nvidia.com/mig-3g.20gb: 500m
`

func TestRules(t *testing.T) {
	fs := lintDoc(t, fullPod)

	legacy := find(t, fs, "DRA001")
	if legacy.Severity != Error || !legacy.Fixable || legacy.Container != "check" || legacy.Resource != "nvidia.com/gpu" ||
		legacy.Count == nil || *legacy.Count != 1 || legacy.Line != 27 || legacy.Column != 13 ||
		legacy.Workload() != "Deployment/ml/trainer" || legacy.Path != "spec.template.spec" {
		t.Errorf("unexpected DRA001 finding: %+v", legacy)
	}
	count := map[string]int{}
	for _, f := range fs {
		count[f.RuleID]++
	}
	want := map[string]int{
		"DRA001": 5, // check, main x3, second
		"DRA002": 2, // main request != limit, second request without limit
		"DRA003": 1, // 500m
		"DRA004": 1, // mig-2g.10gb: 0
		"DRA005": 1, // rdma
		"DRA006": 1, // gpu.shared
		"DRA007": 1, // init container
		"DRA101": 2, // gpu.product, gpu.present
		"DRA102": 1,
		"DRA103": 1,
		"DRA104": 1,
		"DRA105": 1,
	}
	for id, n := range want {
		if count[id] != n {
			t.Errorf("%s: got %d findings, want %d", id, count[id], n)
		}
	}
	for id := range count {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected rule %s", id)
		}
	}
	if msg := find(t, fs, "DRA101").Message; !strings.Contains(msg, "productName") {
		t.Errorf("DRA101 should suggest a CEL selector: %s", msg)
	}
	for i := 1; i < len(fs); i++ {
		if fs[i].Line < fs[i-1].Line {
			t.Errorf("findings are not sorted by line")
		}
	}
	s := Summarize(fs)
	if s.Errors != 7 || s.Fixable != 5 || s.Errors+s.Warnings+s.Infos != len(fs) {
		t.Errorf("summary: %+v", s)
	}
}

func TestCleanAndMigratedWorkloadsAreSilent(t *testing.T) {
	migrated := `apiVersion: v1
kind: Pod
metadata: {name: ok}
spec:
  runtimeClassName: nvidia
  nodeSelector:
    nvidia.com/gpu.product: A100
  containers:
  - name: c
    image: x
    resources:
      limits: {cpu: 1}
      claims:
      - name: gpu
  resourceClaims:
  - name: gpu
    resourceClaimTemplateName: t
`
	if fs := lintDoc(t, migrated); len(fs) != 0 {
		t.Errorf("migrated workload should produce no findings, got %s", ids(fs))
	}
	if Failed(nil, Warning) {
		t.Error("no findings must not fail")
	}
}

func TestSpecialDocuments(t *testing.T) {
	fs := lintDoc(t, "metadata:\n  name: {{ .Release.Name }}\n---\nname: {{ .Values.x }}\n")
	if ids(fs) != "DRA900" {
		t.Errorf("helm template: got %q, want one DRA900", ids(fs))
	}
	if Failed(fs, Warning) || !Failed(fs, Info) {
		t.Error("DRA900 is informational")
	}
	fs = lintDoc(t, "a: [1, 2\n")
	if ids(fs) != "DRA901" || !Failed(fs, Error) {
		t.Errorf("parse error: got %q", ids(fs))
	}
}

func TestCustomResourceAndMappingFile(t *testing.T) {
	doc := `apiVersion: kubeflow.org/v1
kind: PyTorchJob
metadata: {name: j}
spec:
  pytorchReplicaSpecs:
    Worker:
      template:
        spec:
          tolerations:
          - operator: Exists
          containers:
          - name: pytorch
            image: x
            resources:
              limits:
                example.com/fpga: 1
`
	if fs := lintDoc(t, doc); len(fs) != 0 {
		t.Errorf("unmapped non-NVIDIA resource must be ignored, got %s", ids(fs))
	}
	m, err := mapping.New(mapping.Options{Custom: []mapping.Rule{{Resource: "example.com/fpga", DeviceClass: "fpga.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	fs := Run([]*manifest.File{manifest.Parse("t.yaml", "t.yaml", []byte(doc))}, Options{Mapper: m})
	if ids(fs) != "DRA106 DRA001" {
		t.Errorf("got %q, want DRA106 DRA001", ids(fs))
	}
	if !strings.Contains(find(t, fs, "DRA001").Message, "fpga.example.com") {
		t.Error("DRA001 should name the mapped device class")
	}
}

func TestSeverity(t *testing.T) {
	for in, want := range map[string]Severity{"error": Error, "WARNING": Warning, "warn": Warning, "info": Info, "none": ""} {
		got, err := ParseSeverity(in)
		if err != nil || got != want {
			t.Errorf("ParseSeverity(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseSeverity("fatal"); err == nil {
		t.Error("expected an error")
	}
	fs := []Finding{{Severity: Info}, {Severity: Warning}}
	if Failed(fs, Error) || !Failed(fs, Warning) || !Failed(fs, Info) || Failed(fs, "") {
		t.Error("Failed threshold logic is wrong")
	}
	seen := map[string]bool{}
	for _, r := range AllRules() {
		if seen[r.ID] || r.Name == "" || r.Summary == "" || r.Help == "" || r.Severity.Rank() == 0 {
			t.Errorf("rule %s is incomplete or duplicated", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestNoPositions(t *testing.T) {
	f := manifest.Parse("cluster://ns/Pod/p", "p", []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/gpu: 1\n"))
	for _, fd := range Run([]*manifest.File{f}, Options{NoPositions: true}) {
		if fd.Line != 0 || fd.Column != 0 {
			t.Errorf("unexpected position in %+v", fd)
		}
	}
}
