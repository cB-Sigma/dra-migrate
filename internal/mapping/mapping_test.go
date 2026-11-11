package mapping

import (
	"strings"
	"testing"
)

func TestBuiltin(t *testing.T) {
	m := Default()
	tests := []struct {
		resource string
		ok       bool
		class    string
		claim    string
		shared   bool
		selector string
	}{
		{"nvidia.com/gpu", true, "gpu.nvidia.com", "gpu", false, ""},
		{"nvidia.com/gpu.shared", true, "gpu.nvidia.com", "gpu-shared", true, ""},
		{"nvidia.com/mig-1g.10gb", true, "mig.nvidia.com", "mig-1g-10gb", false, "device.attributes['gpu.nvidia.com'].profile == '1g.10gb'"},
		{"nvidia.com/mig-3g.20gb.shared", true, "mig.nvidia.com", "mig-3g-20gb-shared", true, "device.attributes['gpu.nvidia.com'].profile == '3g.20gb'"},
		{"nvidia.com/mig-1g.5gb+me", true, "mig.nvidia.com", "mig-1g-5gb-me", false, "device.attributes['gpu.nvidia.com'].profile == '1g.5gb+me'"},
		{"nvidia.com/rdma_shared_device_a", false, "", "", false, ""},
		{"amd.com/gpu", false, "", "", false, ""},
		{"cpu", false, "", "", false, ""},
	}
	for _, tt := range tests {
		res, ok := m.Resolve(tt.resource)
		if ok != tt.ok {
			t.Errorf("%s: ok = %v", tt.resource, ok)
			continue
		}
		if !ok {
			continue
		}
		if res.DeviceClass != tt.class || res.ClaimName != tt.claim || res.Shared != tt.shared {
			t.Errorf("%s: got %+v", tt.resource, res)
		}
		if tt.shared != (res.CountMode == CountSingle) {
			t.Errorf("%s: count mode %q", tt.resource, res.CountMode)
		}
		if got := strings.Join(res.Selectors, ";"); got != tt.selector {
			t.Errorf("%s: selectors = %q", tt.resource, got)
		}
		if !isDNSLabel(res.ClaimName) || !isDNSLabel(res.RequestName) {
			t.Errorf("%s: names are not DNS labels: %q %q", tt.resource, res.ClaimName, res.RequestName)
		}
	}
	if !m.Candidate("nvidia.com/rdma_shared_device_a") || m.Candidate("amd.com/gpu") {
		t.Error("Candidate should flag unknown nvidia.com resources only")
	}
}

func TestOptions(t *testing.T) {
	m, err := New(Options{DeviceClass: "gpu.example.org", Sharing: SharingTimeSlicing})
	if err != nil {
		t.Fatal(err)
	}
	gpu, _ := m.Resolve("nvidia.com/gpu")
	if gpu.DeviceClass != "gpu.example.org" || gpu.Config != nil {
		t.Errorf("gpu: %+v", gpu)
	}
	shared, _ := m.Resolve("nvidia.com/gpu.shared")
	if shared.Config == nil || shared.Config.Parameters["kind"] != "GpuConfig" {
		t.Errorf("shared: %+v", shared.Config)
	}
	mig, _ := m.Resolve("nvidia.com/mig-1g.5gb.shared")
	if mig.Config != nil {
		t.Error("time-slicing must not be configured for MIG devices")
	}
	if mig.DeviceClass != NvidiaMIGClass {
		t.Error("--device-class must not change the MIG class")
	}
	if _, err := New(Options{Sharing: "bogus"}); err == nil {
		t.Error("expected an error for an unknown sharing strategy")
	}
	if _, err := New(Options{DeviceClass: "Not_Valid"}); err == nil {
		t.Error("expected an error for an invalid device class")
	}
}

func TestMappingFile(t *testing.T) {
	rules, err := Parse([]byte(`
mappings:
- resource: example.com/fpga-*
  deviceClass: fpga.example.com
  selectors: ["device.attributes['fpga.example.com'].family == 'a10'"]
  countMode: single
- resource: nvidia.com/gpu
  deviceClass: big-gpus.example.com
  claimName: big
`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Custom: rules})
	if err != nil {
		t.Fatal(err)
	}
	fpga, ok := m.Resolve("example.com/fpga-arria10")
	if !ok || fpga.Kind != KindCustom || fpga.ClaimName != "fpga-arria10" || fpga.RequestName != "fpga-arria10" || fpga.CountMode != CountSingle {
		t.Errorf("fpga: %+v", fpga)
	}
	if gpu, _ := m.Resolve("nvidia.com/gpu"); gpu.DeviceClass != "big-gpus.example.com" || gpu.ClaimName != "big" {
		t.Errorf("custom rules must win over built-ins: %+v", gpu)
	}
	if _, ok := m.Resolve("example.com/other"); ok {
		t.Error("unexpected match")
	}

	for name, bad := range map[string]string{
		"unknown field":     "mappings:\n- resource: a.b/c\n  deviceClass: x\n  devcieClass: y\n",
		"no mappings":       "mappings: []\n",
		"missing class":     "mappings:\n- resource: a.b/c\n",
		"unqualified":       "mappings:\n- resource: gpu\n  deviceClass: x\n",
		"bad claim name":    "mappings:\n- resource: a.b/c\n  deviceClass: x\n  claimName: Not_OK\n",
		"bad count mode":    "mappings:\n- resource: a.b/c\n  deviceClass: x\n  countMode: many\n",
		"incomplete config": "mappings:\n- resource: a.b/c\n  deviceClass: x\n  config: {driver: d}\n",
		"bad pattern":       "mappings:\n- resource: a.b/[c\n  deviceClass: x\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSelectorForNodeLabel(t *testing.T) {
	tests := []struct {
		key, value, want string
	}{
		{LabelGPUProduct, "A100-SXM4-40GB", "device.attributes['gpu.nvidia.com'].productName.lowerAscii().matches('a100[ -]sxm4[ -]40gb')"},
		{LabelGPUProduct, "NVIDIA-A100-SXM4-40GB-SHARED", "device.attributes['gpu.nvidia.com'].productName.lowerAscii().matches('a100[ -]sxm4[ -]40gb')"},
		{LabelGPUProduct, "Tesla-T4", "device.attributes['gpu.nvidia.com'].productName.lowerAscii().matches('tesla[ -]t4')"},
		{LabelGPUProduct, "it's", ""},
		{LabelGPUMemory, "40960", "device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('40960Mi')) >= 0"},
		{LabelGPUMemory, "lots", ""},
		{LabelGPUFamily, "ampere", "device.attributes['gpu.nvidia.com'].architecture.lowerAscii() == 'ampere'"},
		{"nvidia.com/gpu.count", "8", ""},
	}
	for _, tt := range tests {
		got, ok := SelectorForNodeLabel(tt.key, tt.value)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("%s=%s: got %q, %v; want %q", tt.key, tt.value, got, ok, tt.want)
		}
	}
	if DeviceLabel("nvidia.com/gpu.count") || !DeviceLabel(LabelGPUProduct) {
		t.Error("DeviceLabel is wrong")
	}
}

func TestSanitizeLabel(t *testing.T) {
	for in, want := range map[string]string{
		"mig-1g.10gb":           "mig-1g-10gb",
		"GPU_Shared":            "gpu-shared",
		"---":                   "fb",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
	} {
		if got := SanitizeLabel(in, "fb"); got != want {
			t.Errorf("SanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
