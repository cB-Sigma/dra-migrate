package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/cb-Sigma/dra-migrate/internal/cluster"
	"github.com/cb-Sigma/dra-migrate/internal/verify"
)

const legacyPod = `apiVersion: v1
kind: Pod
metadata:
  name: p
spec:
  containers:
  - name: c
    image: x
    resources:
      limits:
        nvidia.com/gpu: 1
  tolerations:
  - key: nvidia.com/gpu
    operator: Exists
`

const cleanPod = `apiVersion: v1
kind: Pod
metadata:
  name: clean
spec:
  containers:
  - name: c
    image: x
`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLintExitCodes(t *testing.T) {
	dir := writeTree(t, map[string]string{"legacy.yaml": legacyPod, "clean.yaml": cleanPod, "bad/map.yaml": "mappings: 1\n"})

	code, out, _ := run(t, dir, "", "lint", "clean.yaml")
	if code != ExitOK || !strings.Contains(out, "No legacy device requests found") {
		t.Errorf("clean: code=%d out=%q", code, out)
	}
	code, out, _ = run(t, "", "", "lint", ".")
	if code != ExitFindings || !strings.Contains(out, "legacy.yaml:11:9: error DRA001 Pod/p") {
		t.Errorf("legacy: code=%d out=%q", code, out)
	}
	if code, _, _ = run(t, "", "", "lint", "--fail-on", "none", "legacy.yaml"); code != ExitOK {
		t.Errorf("--fail-on none: code=%d", code)
	}
	if code, _, _ = run(t, "", legacyPod, "lint", "-"); code != ExitFindings {
		t.Errorf("stdin: code=%d", code)
	}
	code, out, _ = run(t, "", legacyPod, "lint")
	if code != ExitFindings || !strings.Contains(out, "<stdin>:11:9") {
		t.Errorf("stdin without args: code=%d out=%q", code, out)
	}
	for _, args := range [][]string{
		{"lint", "missing.yaml"},
		{"lint", "--format", "xml", "clean.yaml"},
		{"lint", "--fail-on", "sometimes", "clean.yaml"},
		{"lint", "--mapping-file", "bad/map.yaml", "clean.yaml"},
		{"lint", "--mapping-file", "nope.yaml", "clean.yaml"},
		{"lint", "--no-such-flag"},
		{"frobnicate"},
	} {
		code, _, stderr := run(t, "", "", args...)
		if code != ExitError || !strings.HasPrefix(stderr, "Error: ") {
			t.Errorf("%v: code=%d stderr=%q, want exit %d with an error", args, code, stderr, ExitError)
		}
	}
}

func TestLintFormats(t *testing.T) {
	dir := writeTree(t, map[string]string{"legacy.yaml": legacyPod})
	_, out, _ := run(t, dir, "", "lint", "-f", "json", "legacy.yaml")
	var res struct {
		Findings []map[string]any
		Summary  map[string]int
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Findings) != 1 || res.Summary["errors"] != 1 {
		t.Errorf("json: %v %s", err, out)
	}
	_, out, _ = run(t, "", "", "lint", "-f", "github", "legacy.yaml")
	if !strings.HasPrefix(out, "::error file=legacy.yaml,line=11,col=9,title=DRA001 legacy-device-request::") {
		t.Errorf("github: %s", out)
	}
	_, out, _ = run(t, "", "", "lint", "-f", "sarif", "legacy.yaml")
	var sarif map[string]any
	if err := json.Unmarshal([]byte(out), &sarif); err != nil || sarif["version"] != "2.1.0" {
		t.Errorf("sarif: %v %s", err, out)
	}
}

func TestHelmPipeline(t *testing.T) {
	// A raw chart template is detected and skipped...
	raw := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: {{ include \"chart.fullname\" . }}\nspec:\n  template:\n    spec:\n      containers:\n      - name: c\n        resources:\n          limits:\n            nvidia.com/gpu: {{ .Values.gpus }}\n"
	dir := writeTree(t, map[string]string{"chart/templates/deployment.yaml": raw})
	code, out, _ := run(t, dir, "", "lint", "chart")
	if code != ExitOK || !strings.Contains(out, "DRA900") || strings.Contains(out, "DRA001") || !strings.Contains(out, "helm template") {
		t.Errorf("raw template: code=%d out=%s", code, out)
	}
	code, out, stderr := run(t, "", "", "convert", "chart")
	if code != ExitOK || out != raw || !strings.Contains(stderr, "unrendered Helm template left untouched") {
		t.Errorf("convert of a raw template must leave it alone: code=%d stderr=%s out=%s", code, stderr, out)
	}
	// ...while rendered output on stdin is linted and converted normally.
	rendered := read(t, filepath.Join(goldenAbs, "helm-rendered", "input.yaml"))
	code, out, _ = run(t, "", rendered, "lint", "-")
	for _, want := range []string{"<stdin>:50:15: error DRA001 Deployment/release-embeddings", "DRA103", "DRA105", "DRA104"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered lint output misses %q:\n%s", want, out)
		}
	}
	if code != ExitFindings || strings.Contains(out, "DRA900") {
		t.Errorf("rendered: code=%d out=%s", code, out)
	}
	code, out, _ = run(t, "", rendered, "convert", "-q", "-")
	if code != ExitOK || !strings.Contains(out, "# Source: embeddings/templates/deployment.yaml") {
		t.Errorf("convert of rendered output: code=%d", code)
	}
	if _, err := verify.Manifests([]byte(strings.ReplaceAll(out, "apiVersion: monitoring.coreos.com/v1", "apiVersion: example.com/v1"))); err != nil {
		t.Errorf("converted rendered chart is not valid: %v", err)
	}
}

// goldenAbs is resolved before any test changes the working directory.
var goldenAbs, _ = filepath.Abs(goldenDir)

func TestConvertOutputModes(t *testing.T) {
	files := map[string]string{"a/legacy.yaml": legacyPod, "a/sub/clean.yaml": cleanPod, "single.yaml": legacyPod}
	dir := writeTree(t, files)

	// stdout: nothing is written to disk.
	code, out, stderr := run(t, dir, "", "convert", "a")
	if code != ExitOK || !strings.Contains(out, "kind: ResourceClaimTemplate") || !strings.Contains(out, "name: clean") {
		t.Fatalf("stdout mode: code=%d out=%s", code, out)
	}
	if !strings.Contains(stderr, "rewrote 1 container(s) in 1 file(s), generated 1 ResourceClaimTemplate(s) (resource.k8s.io/v1)") {
		t.Errorf("summary missing: %s", stderr)
	}
	if _, err := verify.Manifests([]byte(out)); err != nil {
		t.Errorf("concatenated stdout is not a valid YAML stream: %v\n%s", err, out)
	}
	if read(t, "a/legacy.yaml") != legacyPod {
		t.Error("stdout mode modified the input file")
	}

	// --diff
	code, out, _ = run(t, "", "", "convert", "--diff", "a")
	if code != ExitOK || !strings.Contains(out, "--- a/a/legacy.yaml") || strings.Contains(out, "clean.yaml") {
		t.Errorf("diff: code=%d out=%s", code, out)
	}
	if read(t, "a/legacy.yaml") != legacyPod {
		t.Error("--diff modified the input file")
	}

	// --output-dir mirrors the directory layout.
	code, _, _ = run(t, "", "", "convert", "-q", "-o", "out", "a", "single.yaml")
	if code != ExitOK {
		t.Fatalf("output-dir: code=%d", code)
	}
	if !strings.Contains(read(t, "out/legacy.yaml"), "resourceClaimTemplateName: p-gpu") || read(t, "out/sub/clean.yaml") != cleanPod ||
		!strings.Contains(read(t, "out/single.yaml"), "kind: ResourceClaimTemplate") {
		t.Error("output-dir content is wrong")
	}
	code, _, _ = run(t, "", legacyPod, "convert", "-q", "-o", "out2", "-")
	if code != ExitOK || !strings.Contains(read(t, "out2/stdin.yaml"), "kind: ResourceClaimTemplate") {
		t.Error("stdin with --output-dir should write stdin.yaml")
	}

	// --in-place rewrites only files that change, and is idempotent.
	before, err := os.Stat("a/sub/clean.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ = run(t, "", "", "convert", "-i", "a"); code != ExitOK {
		t.Fatalf("in-place: code=%d", code)
	}
	converted := read(t, "a/legacy.yaml")
	if !strings.Contains(converted, "kind: ResourceClaimTemplate") || strings.Contains(converted, "nvidia.com/gpu: 1") {
		t.Errorf("in-place result:\n%s", converted)
	}
	after, err := os.Stat("a/sub/clean.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("in-place rewrote an unchanged file")
	}
	code, _, stderr = run(t, "", "", "convert", "-i", "a")
	if code != ExitOK || !strings.Contains(stderr, "nothing to convert") || read(t, "a/legacy.yaml") != converted {
		t.Errorf("second in-place run must be a no-op: %s", stderr)
	}
	if code, _, _ = run(t, "", "", "lint", "a"); code != ExitOK {
		t.Error("lint must pass after convert --in-place")
	}

	// Invalid combinations.
	for _, args := range [][]string{
		{"convert", "-i", "--diff", "a"},
		{"convert", "-i", "-o", "x", "a"},
		{"convert", "-i", "-"},
		{"convert", "--api-version", "v2", "single.yaml"},
		{"convert", "--claim-mode", "maybe", "single.yaml"},
		{"convert", "--sharing", "magic", "single.yaml"},
		{"convert", "--device-class", "Bad_Name", "single.yaml"},
	} {
		if code, _, stderr := run(t, "", "", args...); code != ExitError || stderr == "" {
			t.Errorf("%v: code=%d, want %d", args, code, ExitError)
		}
	}
}

func TestConvertDeviceClassFlag(t *testing.T) {
	_, out, _ := run(t, "", legacyPod, "convert", "-q", "--device-class", "gpu.example.com", "-")
	if !strings.Contains(out, "deviceClassName: gpu.example.com") {
		t.Errorf("--device-class not applied:\n%s", out)
	}
}

func TestExplainAndVersion(t *testing.T) {
	code, out, _ := run(t, "", "", "explain")
	if code != ExitOK || !strings.Contains(out, "resourceClaimTemplateName") || !strings.Contains(out, "DRAExtendedResource") {
		t.Errorf("explain: code=%d", code)
	}
	// The YAML in the guide must itself be valid.
	_, after, _ := strings.Cut(out, "After: a claim on a class of devices")
	block, _, _ := strings.Cut(after, "    ---\n")
	var doc strings.Builder
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "    ") {
			doc.WriteString(strings.TrimPrefix(l, "    ") + "\n")
		}
	}
	if sum, err := verify.Manifests([]byte(doc.String())); err != nil || len(sum.Templates) != 1 {
		t.Errorf("example in explain text is not a valid ResourceClaimTemplate: %v\n%s", err, doc.String())
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, _ = run(t, "", "", args...)
		if code != ExitOK || !strings.HasPrefix(out, "dra-migrate ") {
			t.Errorf("%v: code=%d out=%q", args, code, out)
		}
	}
	if code, out, _ = run(t, "", "", "--help"); code != ExitOK || !strings.Contains(out, "convert") {
		t.Errorf("help: code=%d", code)
	}
}

func fakeCluster(t *testing.T, docs ...string) {
	t.Helper()
	var objs []runtime.Object
	for _, d := range docs {
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(d), &u.Object); err != nil {
			t.Fatal(err)
		}
		objs = append(objs, u)
	}
	kinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "nodes"}:                                    "NodeList",
		{Version: "v1", Resource: "pods"}:                                     "PodList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:               "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}:              "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:                "DaemonSetList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:               "ReplicaSetList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:                     "JobList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:                 "CronJobList",
		{Group: "resource.k8s.io", Version: "v1", Resource: "deviceclasses"}:  "DeviceClassList",
		{Group: "resource.k8s.io", Version: "v1", Resource: "resourceslices"}: "ResourceSliceList",
	}
	disc := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	disc.FakedServerVersion = &version.Info{Major: "1", Minor: "34", GitVersion: "v1.34.1"}
	disc.Resources = []*metav1.APIResourceList{{GroupVersion: "v1"}, {GroupVersion: "resource.k8s.io/v1"}}
	orig := newClients
	t.Cleanup(func() { newClients = orig })
	newClients = func(string, string) (cluster.Clients, error) {
		return cluster.Clients{Discovery: disc, Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)}, nil
	}
}

const (
	clusterClass = "apiVersion: resource.k8s.io/v1\nkind: DeviceClass\nmetadata: {name: gpu.nvidia.com}\nspec: {}\n"
	clusterSlice = "apiVersion: resource.k8s.io/v1\nkind: ResourceSlice\nmetadata: {name: s}\nspec: {driver: gpu.nvidia.com}\n"
)

func TestClusterCommand(t *testing.T) {
	podDoc := strings.Replace(legacyPod, "  name: p\n", "  name: p\n  namespace: team-a\n", 1)

	fakeCluster(t, clusterClass, clusterSlice, podDoc)
	code, out, stderr := run(t, "", "", "cluster")
	if code != ExitFindings {
		t.Fatalf("code=%d stderr=%s out=%s", code, stderr, out)
	}
	for _, want := range []string{
		"DRA readiness (server v1.34.1)",
		"[ok     ] resource-api: resource.k8s.io serves v1",
		"[ok     ] device-classes",
		"[ok     ] resource-slices: 1 ResourceSlice(s)",
		"Workloads (1 scanned)",
		"cluster://team-a/Pod/p: error DRA001 Pod/team-a/p",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	_, out, _ = run(t, "", "", "cluster", "-f", "json")
	var res struct {
		Readiness cluster.Readiness
		Findings  []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Findings) != 1 || res.Readiness.ServerVersion != "v1.34.1" {
		t.Errorf("json: %v\n%s", err, out)
	}

	// A migrated cluster that is ready exits 0.
	fakeCluster(t, clusterClass, clusterSlice)
	if code, out, _ = run(t, "", "", "cluster"); code != ExitOK || !strings.Contains(out, "No legacy device requests found") {
		t.Errorf("ready cluster: code=%d out=%s", code, out)
	}
	// No workloads to fix, but the driver is missing: not ready.
	fakeCluster(t)
	code, out, _ = run(t, "", "", "cluster")
	if code != ExitFindings || !strings.Contains(out, "[fail   ] device-classes") {
		t.Errorf("unready cluster: code=%d out=%s", code, out)
	}
	if code, _, _ = run(t, "", "", "cluster", "--skip-readiness"); code != ExitOK {
		t.Errorf("--skip-readiness: code=%d", code)
	}

	newClients = func(string, string) (cluster.Clients, error) { return cluster.Clients{}, errors.New("no kubeconfig") }
	if code, _, stderr = run(t, "", "", "cluster"); code != ExitError || !strings.Contains(stderr, "no kubeconfig") {
		t.Errorf("client error: code=%d stderr=%s", code, stderr)
	}
}
