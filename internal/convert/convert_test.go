package convert

import (
	"strings"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/verify"
)

func convertDoc(t *testing.T, doc string, opts Options) *FileResult {
	t.Helper()
	res, err := Convert([]*manifest.File{manifest.Parse("test.yaml", "test.yaml", []byte(doc))}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res.Files[0]
}

// mustVerify strictly decodes the output and returns its summary.
func mustVerify(t *testing.T, out []byte) *verify.Summary {
	t.Helper()
	sum, err := verify.Manifests(out)
	if err != nil {
		t.Fatalf("output is not schema-valid: %v\n%s", err, out)
	}
	return sum
}

// layouts are the same Deployment written in different but equally valid
// YAML styles. All of them must be patched textually.
var layouts = map[string]string{
	"compact sequences": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: 2
            cpu: 1
`,
	"indented sequences": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
        - name: main
          image: x
          resources:
            limits:
              cpu: 1
              nvidia.com/gpu: 2
`,
	"four space indent": `apiVersion: apps/v1
kind: Deployment
metadata:
    name: app
spec:
    template:
        spec:
            containers:
                - name: main
                  image: x
                  resources:
                      limits:
                          nvidia.com/gpu: 2
`,
	"no trailing newline": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: 2`,
	"comments and blank lines": `# top

apiVersion: apps/v1
kind: Deployment
metadata:
  name: app   # trailing
spec:
  template:
    spec:

      containers:
      - name: main
        image: x
        resources:
          # resources comment
          limits:
            nvidia.com/gpu: 2   # two of them

          requests:

            # request side
            nvidia.com/gpu: 2
            memory: 1Gi

      # trailing comment of the pod spec

`,
	"quoted keys and values": `"apiVersion": apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      "containers":
      - name: 'main'
        image: "x"
        "resources":
          "limits":
            "nvidia.com/gpu": '2'
`,
	"resources first on the dash line": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - resources:
          limits:
            nvidia.com/gpu: 2
        name: main
        image: x
`,
	"pod spec ends with a block scalar": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: 2
        args:
        - |
          echo one
          # not a comment
          echo two
`,
	"document separators": `---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: 2
---
`,
}

func TestLayoutsArePatchedTextually(t *testing.T) {
	for name, doc := range layouts {
		for _, crlf := range []bool{false, true} {
			in := doc
			if crlf {
				name += " (CRLF)"
				in = strings.ReplaceAll(doc, "\n", "\r\n")
			}
			t.Run(name, func(t *testing.T) {
				fr := convertDoc(t, in, Options{})
				if !fr.Changed || fr.Containers != 1 || fr.Generated != 1 {
					t.Fatalf("unexpected result: changed=%v containers=%d generated=%d", fr.Changed, fr.Containers, fr.Generated)
				}
				if fr.Reencoded != 0 {
					t.Errorf("document was re-encoded instead of patched:\n%s", fr.Output)
				}
				sum := mustVerify(t, fr.Output)
				if len(sum.Requests) != 1 || sum.Requests[0].Count != 2 || sum.Requests[0].DeviceClass != "gpu.nvidia.com" {
					t.Errorf("requests: %+v", sum.Requests)
				}
				if len(sum.LegacyResources) != 0 {
					t.Errorf("legacy resources left: %v", sum.LegacyResources)
				}
				// Every line of the input that does not mention the GPU or a
				// mapping that became empty survives unchanged.
				out := string(fr.Output)
				for _, line := range strings.Split(in, "\n") {
					trimmed := strings.TrimSpace(line)
					if trimmed == "" || strings.Contains(line, "nvidia.com/gpu") ||
						strings.Contains(trimmed, "limits") || strings.Contains(trimmed, "requests") || strings.HasPrefix(trimmed, "#") {
						continue
					}
					if !strings.Contains(out, line+"\n") && !strings.HasSuffix(out, line) {
						t.Errorf("line %q was modified or lost:\n%s", line, out)
					}
				}
				if crlf && strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
					t.Errorf("CRLF file must keep CRLF line endings everywhere:\n%q", out)
				}
				// Idempotence.
				again := convertDoc(t, out, Options{})
				if again.Changed {
					t.Errorf("second conversion changed the output:\n%s", again.Output)
				}
			})
		}
	}
}

func TestCommentsArePreserved(t *testing.T) {
	fr := convertDoc(t, layouts["comments and blank lines"], Options{})
	out := string(fr.Output)
	for _, want := range []string{"# top", "# trailing", "# resources comment", "# two of them", "# trailing comment of the pod spec", "memory: 1Gi"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "# top\n\napiVersion: resource.k8s.io/v1\n") {
		t.Errorf("file header comment must stay on top:\n%s", out)
	}
}

func TestFallbackReencodes(t *testing.T) {
	for name, doc := range map[string]string{
		"flow mapping":     "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - {name: c, image: x, resources: {limits: {nvidia.com/gpu: 1}}}\n",
		"tabs in a scalar": "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n    image: x\n    args: [\"a\tb\"]\n    resources:\n      limits:\n        nvidia.com/gpu: 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			fr := convertDoc(t, doc, Options{})
			if !fr.Changed {
				t.Fatal("not converted")
			}
			sum := mustVerify(t, fr.Output)
			if len(sum.LegacyResources) != 0 || len(sum.PodClaims) == 0 {
				t.Errorf("conversion incomplete: %+v\n%s", sum, fr.Output)
			}
			if again := convertDoc(t, string(fr.Output), Options{}); again.Changed {
				t.Errorf("not idempotent:\n%s", again.Output)
			}
		})
	}
	fr := convertDoc(t, "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - {name: c, image: x, resources: {limits: {nvidia.com/gpu: 1}}}\n", Options{})
	if fr.Reencoded != 1 {
		t.Errorf("Reencoded = %d, want 1", fr.Reencoded)
	}
}

const twoContainers = `apiVersion: v1
kind: Pod
metadata:
  name: p
  namespace: ns
spec:
  initContainers:
  - name: init
    image: x
    resources:
      limits:
        nvidia.com/gpu: 3
  containers:
  - name: a
    image: x
    resources:
      limits:
        nvidia.com/gpu: 1
  - name: b
    image: x
    resources:
      limits:
        nvidia.com/gpu: 2
`

func devices(sum *verify.Summary) (total int64) {
	byObject := map[string]int64{}
	for _, r := range sum.Requests {
		byObject[strings.SplitN(r.Object, "/", 2)[1]] += r.Count
	}
	for _, pc := range sum.PodClaims {
		total += byObject[pc.Namespace+"/"+pc.Template+pc.Claim]
	}
	return total
}

func TestClaimPlanning(t *testing.T) {
	t.Run("one claim per container", func(t *testing.T) {
		fr := convertDoc(t, twoContainers, Options{})
		sum := mustVerify(t, fr.Output)
		// a: 1, b: 2 and the init container (3) fits in neither, so it
		// gets its own claim and a warning.
		if len(sum.PodClaims) != 3 || devices(sum) != 6 {
			t.Errorf("claims=%d devices=%d\n%s", len(sum.PodClaims), devices(sum), fr.Output)
		}
		if !hasNote(fr, LevelWarning, "gets its own claim") {
			t.Errorf("expected a warning about the init container: %v", fr.Notes)
		}
		for _, pc := range sum.PodClaims {
			if !sum.Templates["ns/"+pc.Template] {
				t.Errorf("template %q not generated in namespace ns", pc.Template)
			}
		}
	})
	t.Run("share across containers", func(t *testing.T) {
		fr := convertDoc(t, twoContainers, Options{ShareAcrossContainers: true})
		sum := mustVerify(t, fr.Output)
		// max(a, b, init) = 3 devices in a single claim.
		if len(sum.PodClaims) != 1 || devices(sum) != 3 {
			t.Errorf("claims=%d devices=%d\n%s", len(sum.PodClaims), devices(sum), fr.Output)
		}
	})
	t.Run("init container reuses a fitting claim", func(t *testing.T) {
		doc := strings.Replace(twoContainers, "nvidia.com/gpu: 3", "nvidia.com/gpu: 2", 1)
		fr := convertDoc(t, doc, Options{})
		sum := mustVerify(t, fr.Output)
		if len(sum.PodClaims) != 2 || devices(sum) != 3 {
			t.Errorf("claims=%d devices=%d\n%s", len(sum.PodClaims), devices(sum), fr.Output)
		}
		if !hasNote(fr, LevelInfo, `init container "init" reuses the claim of container "b"`) {
			t.Errorf("notes: %v", fr.Notes)
		}
	})
	t.Run("claim mode", func(t *testing.T) {
		fr := convertDoc(t, twoContainers, Options{ClaimMode: ModeClaim})
		sum := mustVerify(t, fr.Output)
		if len(sum.Claims) != 3 || len(sum.Templates) != 0 {
			t.Errorf("claims=%v templates=%v", sum.Claims, sum.Templates)
		}
		for _, pc := range sum.PodClaims {
			if pc.Claim == "" || !sum.Claims["ns/"+pc.Claim] {
				t.Errorf("pod claim %+v does not reference a generated ResourceClaim", pc)
			}
		}
		if !hasNote(fr, LevelWarning, "shared by every pod") {
			t.Errorf("expected a warning about shared claims: %v", fr.Notes)
		}
	})
}

func hasNote(fr *FileResult, level Level, substr string) bool {
	for _, n := range fr.Notes {
		if n.Level == level && strings.Contains(n.Message, substr) {
			return true
		}
	}
	return false
}

func TestAPIVersions(t *testing.T) {
	for _, v := range []string{"v1", "v1beta1", "v1beta2", "resource.k8s.io/v1beta2"} {
		fr := convertDoc(t, twoContainers, Options{APIVersion: v})
		short := strings.TrimPrefix(v, "resource.k8s.io/")
		if !strings.Contains(string(fr.Output), "apiVersion: resource.k8s.io/"+short+"\n") {
			t.Errorf("%s: wrong apiVersion in output", v)
		}
		if got := strings.Contains(string(fr.Output), "exactly:"); got != (short != "v1beta1") {
			t.Errorf("%s: 'exactly' present = %v", v, got)
		}
		mustVerify(t, fr.Output)
	}
	if _, err := Convert(nil, Options{APIVersion: "v1alpha3"}); err == nil {
		t.Error("expected an error for an unsupported API version")
	}
	if _, err := Convert(nil, Options{ClaimMode: "both"}); err == nil {
		t.Error("expected an error for an unsupported claim mode")
	}
}

func TestNamesNeverCollide(t *testing.T) {
	doc := `apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: app-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: something-else.example.com
---
apiVersion: v1
kind: Pod
metadata:
  name: app
spec:
  containers:
  - name: main
    image: x
    resources:
      limits:
        nvidia.com/gpu: 1
  resourceClaims:
  - name: gpu
    resourceClaimName: unrelated
---
apiVersion: batch/v1
kind: Job
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: 1
`
	fr := convertDoc(t, doc, Options{})
	sum := mustVerify(t, fr.Output)
	out := string(fr.Output)
	// The existing app-gpu template has a different spec, so a new name is
	// used; the Pod and the Job have identical requests and share it.
	if !sum.Templates["/app-gpu-2"] || len(sum.Templates) != 2 || fr.Generated != 1 {
		t.Errorf("templates: %v generated=%d\n%s", sum.Templates, fr.Generated, out)
	}
	if strings.Count(out, "resourceClaimTemplateName: app-gpu-2") != 2 {
		t.Errorf("both workloads should reference app-gpu-2:\n%s", out)
	}
	if !strings.Contains(out, "- name: gpu-2\n") {
		t.Errorf("pod claim name must not collide with the existing one:\n%s", out)
	}

	// An existing identical template is reused instead of duplicated.
	reuse := strings.Replace(doc, "something-else.example.com", "gpu.nvidia.com", 1)
	fr = convertDoc(t, reuse, Options{})
	if fr.Generated != 0 || strings.Count(string(fr.Output), "resourceClaimTemplateName: app-gpu\n") != 2 {
		t.Errorf("identical template should be reused (generated=%d):\n%s", fr.Generated, fr.Output)
	}
	mustVerify(t, fr.Output)
}

func TestUntouchedInputs(t *testing.T) {
	cases := map[string]string{
		"helm template":   "apiVersion: v1\nkind: Pod\nmetadata:\n  name: {{ .Release.Name }}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/gpu: 1\n",
		"json":            `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"j"},"spec":{"containers":[{"name":"c","resources":{"limits":{"nvidia.com/gpu":1}}}]}}`,
		"list":            "apiVersion: v1\nkind: List\nitems:\n- apiVersion: v1\n  kind: Pod\n  metadata: {name: a}\n  spec:\n    containers:\n    - name: c\n      resources:\n        limits:\n          nvidia.com/gpu: 1\n",
		"zero quantity":   "apiVersion: v1\nkind: Pod\nmetadata: {name: z}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/gpu: 0\n",
		"bad quantity":    "apiVersion: v1\nkind: Pod\nmetadata: {name: z}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/gpu: 500m\n",
		"unmapped":        "apiVersion: v1\nkind: Pod\nmetadata: {name: z}\nspec:\n  containers:\n  - name: c\n    resources:\n      limits:\n        nvidia.com/hostdev: 1\n        amd.com/gpu: 1\n",
		"parse error":     "apiVersion: v1\nkind: [Pod\n",
		"anchors":         "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n    image: x\n    resources: &res\n      limits:\n        nvidia.com/gpu: 1\n  - name: d\n    image: x\n    resources: *res\n",
		"not kubernetes":  "just: some\nyaml: file\n",
		"empty":           "",
		"only separators": "---\n---\n",
	}
	for name, doc := range cases {
		fr := convertDoc(t, doc, Options{})
		if fr.Changed || string(fr.Output) != doc {
			t.Errorf("%s: input must be left untouched, got:\n%s", name, fr.Output)
		}
	}
	for _, name := range []string{"helm template", "json", "list", "bad quantity", "anchors"} {
		if fr := convertDoc(t, cases[name], Options{}); len(fr.Notes) == 0 {
			t.Errorf("%s: expected a note explaining why nothing was converted", name)
		}
	}
}

func TestMismatchUsesLimit(t *testing.T) {
	doc := "apiVersion: v1\nkind: Pod\nmetadata: {name: m}\nspec:\n  containers:\n  - name: c\n    image: x\n    resources:\n      requests:\n        nvidia.com/gpu: 1\n      limits:\n        nvidia.com/gpu: 3\n"
	fr := convertDoc(t, doc, Options{})
	sum := mustVerify(t, fr.Output)
	if len(sum.Requests) != 1 || sum.Requests[0].Count != 3 || !hasNote(fr, LevelWarning, "using the limit (3)") {
		t.Errorf("requests=%+v notes=%v", sum.Requests, fr.Notes)
	}
}

func TestUnique(t *testing.T) {
	used := map[string]bool{"gpu": true, "gpu-2": true}
	if got := unique("gpu", 63, func(n string) bool { return used[n] }); got != "gpu-3" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("a", 80)
	got := unique(long, 63, func(n string) bool { return n == strings.Repeat("a", 63) })
	if len(got) != 63 || !strings.HasSuffix(got, "-2") {
		t.Errorf("got %q (len %d)", got, len(got))
	}
}

func TestDiff(t *testing.T) {
	fr := convertDoc(t, layouts["compact sequences"], Options{})
	d, err := fr.UnifiedDiff()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- a/test.yaml", "+++ b/test.yaml", "-            nvidia.com/gpu: 2", "+          - name: gpu", "+kind: ResourceClaimTemplate"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff misses %q:\n%s", want, d)
		}
	}
	same := convertDoc(t, "a: 1\n", Options{})
	if d, _ := same.UnifiedDiff(); d != "" {
		t.Errorf("unchanged file must have an empty diff, got %q", d)
	}
}
