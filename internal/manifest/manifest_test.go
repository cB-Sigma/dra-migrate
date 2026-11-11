package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitKeepsEveryByte(t *testing.T) {
	for _, in := range []string{
		"",
		"a: 1\n",
		"a: 1",
		"---\na: 1\n---\nb: 2\n",
		"# header\n\na: 1\n--- # trailing comment\nb: 2\n---\n",
		"a: |\n  ---\n  text\n---\nb: 2",
		"a: 1\r\n---\r\nb: 2\r\n",
	} {
		var sb strings.Builder
		line := 1
		for _, seg := range split(in) {
			if seg.StartLine != line {
				t.Errorf("%q: segment starts at line %d, want %d", in, seg.StartLine, line)
			}
			line += strings.Count(seg.Raw, "\n")
			sb.WriteString(seg.Raw)
		}
		if sb.String() != in {
			t.Errorf("split(%q) lost data: %q", in, sb.String())
		}
	}
	if n := len(split("---\na: 1\n---\nb: 2\n")); n != 2 {
		t.Errorf("got %d segments, want 2", n)
	}
	if n := len(split("a: |\n  ---\n  text\n---\nb: 2")); n != 2 {
		t.Errorf("indented --- must not split: got %d segments", n)
	}
}

const cronJob = `apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
  namespace: ml
spec:
  jobTemplate:
    spec:
      template:
        spec:
          initContainers:
          - name: init
            image: x
          - name: sidecar
            image: x
            restartPolicy: Always
          containers:
          - name: main
            image: x
            resources:
              limits:
                nvidia.com/gpu: "2"
                cpu: 1
              requests:
                nvidia.com/gpu: 2
`

func TestObjectDiscovery(t *testing.T) {
	f := Parse("cj.yaml", "cj.yaml", []byte(cronJob))
	if len(f.Segments) != 1 || len(f.Segments[0].Objects) != 1 {
		t.Fatalf("unexpected parse result: %+v", f)
	}
	obj := f.Segments[0].Objects[0]
	if obj.DisplayName() != "CronJob/ml/nightly" {
		t.Errorf("DisplayName = %q", obj.DisplayName())
	}
	if len(obj.PodSpecs) != 1 {
		t.Fatalf("got %d pod specs", len(obj.PodSpecs))
	}
	ps := obj.PodSpecs[0]
	if ps.Path != "spec.jobTemplate.spec.template.spec" {
		t.Errorf("path = %q", ps.Path)
	}
	if len(ps.Containers) != 3 {
		t.Fatalf("got %d containers", len(ps.Containers))
	}
	if !ps.Containers[0].Init || ps.Containers[0].Sidecar || !ps.Containers[1].Sidecar || ps.Containers[2].Init {
		t.Errorf("container classification wrong: %+v", ps.Containers)
	}
	entries := ps.Containers[2].Entries(func(n string) bool { return strings.HasPrefix(n, "nvidia.com/") })
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	e := entries[0]
	if n, ok := e.Count(); !ok || n != 2 {
		t.Errorf("count = %d, %v", n, ok)
	}
	if e.Limit == nil || e.Request == nil || e.Request.Count != 2 {
		t.Errorf("limit/request not both found: %+v", e)
	}
	if got := f.Segments[0].Line(e.Limit.Key); got != 22 {
		t.Errorf("line = %d, want 22", got)
	}
}

func TestQuantities(t *testing.T) {
	for val, want := range map[string]struct {
		n  int64
		ok bool
	}{
		"1":     {1, true},
		`"4"`:   {4, true},
		"0":     {0, true},
		"1k":    {1000, true},
		"500m":  {0, false},
		"-1":    {0, false},
		"lots":  {0, false},
		"[1,2]": {0, false},
	} {
		doc := "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/gpu: " + val + "\n"
		f := Parse("p.yaml", "p.yaml", []byte(doc))
		es := f.Segments[0].Objects[0].PodSpecs[0].Containers[0].Entries(func(string) bool { return true })
		if len(es) != 1 {
			t.Fatalf("%s: got %d entries", val, len(es))
		}
		n, ok := es[0].Count()
		if n != want.n || ok != want.ok {
			t.Errorf("quantity %s: got (%d, %v), want (%d, %v)", val, n, ok, want.n, want.ok)
		}
	}
}

func TestGenericPodSpecDiscovery(t *testing.T) {
	doc := `apiVersion: ray.io/v1
kind: RayCluster
metadata:
  name: rc
status:
  containers:
  - name: not-a-pod-spec
spec:
  headGroupSpec:
    template:
      spec:
        containers:
        - name: head
          image: ray
  workerGroupSpecs:
  - groupName: gpu
    template:
      spec:
        containers:
        - name: worker
          image: ray
`
	f := Parse("rc.yaml", "rc.yaml", []byte(doc))
	obj := f.Segments[0].Objects[0]
	var paths []string
	for _, ps := range obj.PodSpecs {
		paths = append(paths, ps.Path)
	}
	want := "spec.headGroupSpec.template.spec spec.workerGroupSpecs[0].template.spec"
	if strings.Join(paths, " ") != want {
		t.Errorf("paths = %v, want %s", paths, want)
	}
	if IsBuiltinWorkload(obj.APIVersion, obj.Kind) {
		t.Error("RayCluster must not be a builtin workload")
	}
	if !IsBuiltinWorkload("apps/v1", "Deployment") || IsBuiltinWorkload("argoproj.io/v1alpha1", "Deployment") {
		t.Error("IsBuiltinWorkload is wrong for Deployment")
	}
}

func TestList(t *testing.T) {
	doc := `apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: Pod
  metadata: {name: a}
  spec:
    containers:
    - name: c
      image: x
- apiVersion: v1
  kind: ConfigMap
  metadata: {name: b}
`
	f := Parse("l.yaml", "l.yaml", []byte(doc))
	objs := f.Segments[0].Objects
	if len(objs) != 2 || !objs[0].InList || len(objs[0].PodSpecs) != 1 {
		t.Fatalf("unexpected objects: %+v", objs)
	}
}

func TestHelmDetection(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		helm bool
	}{
		{"value action", "apiVersion: v1\nkind: Pod\nmetadata:\n  name: {{ .Release.Name }}\n", true},
		{"control line", "{{- if .Values.enabled }}\napiVersion: v1\nkind: Pod\n{{- end }}\n", true},
		{"quantity action", "a:\n  limits:\n    nvidia.com/gpu: {{ .Values.gpus }}\n", true},
		{"include with nindent", "metadata:\n  labels:\n    {{- include \"x.labels\" . | nindent 4 }}\n", true},
		{"action inside quoted string", "annotations:\n  summary: \"GPU idle on {{ $labels.instance }}\"\n", false},
		{"action inside block scalar", "data:\n  tpl: |\n    {{ if .X }}yes{{ end }}\n", false},
		{"plain yaml", "a: {b: {c: 1}}\n", false},
	}
	for _, tt := range tests {
		f := Parse("t.yaml", "t.yaml", []byte(tt.doc))
		if f.HelmTemplate != tt.helm {
			t.Errorf("%s: HelmTemplate = %v, want %v", tt.name, f.HelmTemplate, tt.helm)
		}
	}
	// Only the templated document of a stream is skipped.
	f := Parse("t.yaml", "t.yaml", []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: ok}\nspec:\n  containers:\n  - name: c\n---\nname: {{ .X }}\n"))
	if !f.HelmTemplate || f.Segments[0].HelmTemplate || !f.Segments[1].HelmTemplate || len(f.Segments[0].Objects) != 1 {
		t.Errorf("per-document detection failed: %+v", f.Segments)
	}
}

func TestParseErrorAndJSON(t *testing.T) {
	f := Parse("bad.yaml", "bad.yaml", []byte("a: [1, 2\nb: 3\n"))
	if f.Segments[0].Err == nil {
		t.Error("expected a parse error")
	}
	j := Parse("p.json", "p.json", []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"j"},"spec":{"containers":[{"name":"c","resources":{"limits":{"nvidia.com/gpu":1}}}]}}`))
	if !j.JSON || len(j.Segments[0].Objects) != 1 || len(j.Segments[0].Objects[0].PodSpecs) != 1 {
		t.Errorf("JSON not handled: %+v", j)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "sub", ".hidden"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("a: 1\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub", "a.yml"), []byte("a: 1\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub", "notes.txt"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub", ".hidden", "c.yaml"), []byte("a: 1\n"), 0o644))

	files, err := Load([]string{dir, "-"}, strings.NewReader("s: 1\n"))
	must(err)
	var rels []string
	for _, f := range files {
		rels = append(rels, filepath.ToSlash(f.Rel))
	}
	if got := strings.Join(rels, ","); got != "b.yaml,sub/a.yml,stdin.yaml" {
		t.Errorf("loaded %s", got)
	}
	if files[2].Path != StdinName {
		t.Errorf("stdin path = %q", files[2].Path)
	}
	if _, err := Load([]string{filepath.Join(dir, "missing.yaml")}, nil); err == nil {
		t.Error("expected an error for a missing file")
	}
	if _, err := Load([]string{"-", "-"}, strings.NewReader("")); err == nil {
		t.Error("expected an error for stdin given twice")
	}
}

func TestNames(t *testing.T) {
	f := Parse("p.yaml", "p.yaml", []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  generateName: batch-\nspec:\n  containers:\n  - name: c\n"))
	obj := f.Segments[0].Objects[0]
	if obj.BaseName() != "batch" || obj.DisplayName() != "Pod/batch-*" {
		t.Errorf("generateName: BaseName=%q DisplayName=%q", obj.BaseName(), obj.DisplayName())
	}
	f = Parse("p.yaml", "p.yaml", []byte("apiVersion: v1\nkind: Pod\nspec:\n  containers:\n  - name: c\n"))
	obj = f.Segments[0].Objects[0]
	if obj.BaseName() != "pod" || obj.DisplayName() != "Pod/<unnamed>" {
		t.Errorf("unnamed: BaseName=%q DisplayName=%q", obj.BaseName(), obj.DisplayName())
	}
}
