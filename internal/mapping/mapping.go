// Package mapping translates legacy extended resource names (the device
// plugin API) into DRA device requests.
package mapping

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Names published by the NVIDIA DRA driver (kubernetes-sigs/dra-driver-nvidia-gpu,
// formerly NVIDIA/k8s-dra-driver-gpu).
const (
	NvidiaDriver         = "gpu.nvidia.com"
	NvidiaGPUClass       = "gpu.nvidia.com"
	NvidiaMIGClass       = "mig.nvidia.com"
	NvidiaConfigAPI      = "resource.nvidia.com/v1beta1"
	NvidiaResourcePrefix = "nvidia.com/"
)

// Kind classifies a legacy resource.
type Kind string

// Resource kinds.
const (
	KindGPU    Kind = "gpu"
	KindMIG    Kind = "mig"
	KindCustom Kind = "custom"
)

// CountMode says how the legacy quantity translates to a device count.
type CountMode string

const (
	// CountDevices requests as many devices as the legacy quantity.
	CountDevices CountMode = "devices"
	// CountSingle always requests one device. It is used for oversubscribed
	// ("shared") resources, where the legacy quantity counts replicas of a
	// device rather than devices.
	CountSingle CountMode = "single"
)

// OpaqueConfig is driver-specific configuration attached to a request.
type OpaqueConfig struct {
	Driver     string         `yaml:"driver" json:"driver"`
	Parameters map[string]any `yaml:"parameters" json:"parameters"`
}

// Rule maps one resource name (or glob) to a DRA request. It is also the
// on-disk format of entries in a mapping file.
type Rule struct {
	// Resource is an extended resource name. "*" matches any run of
	// characters except "/".
	Resource string `yaml:"resource"`
	// DeviceClass is the DeviceClass to request from.
	DeviceClass string `yaml:"deviceClass"`
	// Selectors are CEL expressions, each becoming one selector.
	Selectors []string `yaml:"selectors,omitempty"`
	// ClaimName is the base name for pod-level claims and generated objects.
	ClaimName string `yaml:"claimName,omitempty"`
	// RequestName is the name of the device request inside the claim.
	RequestName string `yaml:"requestName,omitempty"`
	// CountMode is "devices" (default) or "single".
	CountMode CountMode `yaml:"countMode,omitempty"`
	// Config is optional opaque driver configuration.
	Config *OpaqueConfig `yaml:"config,omitempty"`
	// Notes are printed whenever the rule is applied.
	Notes []string `yaml:"notes,omitempty"`
}

// File is the schema of --mapping-file.
type File struct {
	Mappings []Rule `yaml:"mappings"`
}

// Resolved is the outcome of mapping one concrete resource name.
type Resolved struct {
	Resource    string
	Kind        Kind
	Shared      bool // legacy resource was an oversubscribed ".shared" resource
	DeviceClass string
	Selectors   []string
	ClaimName   string
	RequestName string
	CountMode   CountMode
	Config      *OpaqueConfig
	Notes       []string
	MIGProfile  string
}

// Sharing selects the opaque sharing configuration emitted for resources
// that were oversubscribed under the device plugin.
type Sharing string

// Sharing strategies.
const (
	SharingNone        Sharing = "none"
	SharingTimeSlicing Sharing = "time-slicing"
	SharingMPS         Sharing = "mps"
)

// Options configure a Mapper.
type Options struct {
	// DeviceClass overrides the DeviceClass used for full GPUs.
	DeviceClass string
	// Sharing controls opaque config for ".shared" resources.
	Sharing Sharing
	// Custom rules take precedence over the built-in ones.
	Custom []Rule
}

// Mapper resolves resource names.
type Mapper struct {
	opts Options
}

// New validates opts and returns a Mapper.
func New(opts Options) (*Mapper, error) {
	if opts.Sharing == "" {
		opts.Sharing = SharingNone
	}
	switch opts.Sharing {
	case SharingNone, SharingTimeSlicing, SharingMPS:
	default:
		return nil, fmt.Errorf("unknown sharing strategy %q (want none, time-slicing or mps)", opts.Sharing)
	}
	if opts.DeviceClass != "" && !isDNSSubdomain(opts.DeviceClass) {
		return nil, fmt.Errorf("invalid device class name %q", opts.DeviceClass)
	}
	for i, r := range opts.Custom {
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("mappings[%d]: %w", i, err)
		}
	}
	return &Mapper{opts: opts}, nil
}

// Default returns a Mapper with built-in rules only.
func Default() *Mapper {
	m, _ := New(Options{})
	return m
}

// LoadFile reads a mapping file. Unknown fields are rejected.
func LoadFile(p string) ([]Rule, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes mapping file content.
func Parse(data []byte) ([]Rule, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing mapping file: %w", err)
	}
	if len(f.Mappings) == 0 {
		return nil, errors.New("mapping file contains no mappings")
	}
	for i, r := range f.Mappings {
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("mappings[%d]: %w", i, err)
		}
	}
	return f.Mappings, nil
}

var (
	dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	migRe      = regexp.MustCompile(`^nvidia\.com/mig-([0-9]+g\.[0-9]+gb[a-z0-9.+_-]*?)(\.shared)?$`)
)

func isDNSLabel(s string) bool { return len(s) <= 63 && dnsLabelRe.MatchString(s) }

func isDNSSubdomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !isDNSLabel(part) {
			return false
		}
	}
	return true
}

func (r Rule) validate() error {
	if r.Resource == "" {
		return errors.New("resource is required")
	}
	if !strings.Contains(r.Resource, "/") {
		return fmt.Errorf("resource %q must be a fully qualified extended resource name (domain/name)", r.Resource)
	}
	if _, err := path.Match(r.Resource, "x/y"); err != nil {
		return fmt.Errorf("resource %q: invalid pattern", r.Resource)
	}
	if !isDNSSubdomain(r.DeviceClass) {
		return fmt.Errorf("deviceClass %q must be a DNS subdomain", r.DeviceClass)
	}
	if r.ClaimName != "" && !isDNSLabel(r.ClaimName) {
		return fmt.Errorf("claimName %q must be a DNS label", r.ClaimName)
	}
	if r.RequestName != "" && !isDNSLabel(r.RequestName) {
		return fmt.Errorf("requestName %q must be a DNS label", r.RequestName)
	}
	switch r.CountMode {
	case "", CountDevices, CountSingle:
	default:
		return fmt.Errorf("countMode %q must be %q or %q", r.CountMode, CountDevices, CountSingle)
	}
	for _, s := range r.Selectors {
		if strings.TrimSpace(s) == "" {
			return errors.New("selectors must not be empty strings")
		}
	}
	if r.Config != nil {
		if r.Config.Driver == "" || len(r.Config.Parameters) == 0 {
			return errors.New("config requires driver and parameters")
		}
	}
	return nil
}

// Candidate reports whether name looks like a legacy accelerator resource
// worth reporting even when no rule matches it.
func (m *Mapper) Candidate(name string) bool {
	if _, ok := m.Resolve(name); ok {
		return true
	}
	return strings.HasPrefix(name, NvidiaResourcePrefix)
}

// Resolve maps a resource name. ok is false when no rule applies.
func (m *Mapper) Resolve(name string) (*Resolved, bool) {
	for _, r := range m.opts.Custom {
		if matched, _ := path.Match(r.Resource, name); !matched {
			continue
		}
		res := &Resolved{
			Resource:    name,
			Kind:        KindCustom,
			DeviceClass: r.DeviceClass,
			Selectors:   append([]string(nil), r.Selectors...),
			ClaimName:   r.ClaimName,
			RequestName: r.RequestName,
			CountMode:   r.CountMode,
			Config:      r.Config,
			Notes:       append([]string(nil), r.Notes...),
		}
		if res.ClaimName == "" {
			res.ClaimName = SanitizeLabel(name[strings.LastIndex(name, "/")+1:], "device")
		}
		if res.RequestName == "" {
			res.RequestName = res.ClaimName
		}
		if res.CountMode == "" {
			res.CountMode = CountDevices
		}
		return res, true
	}
	return m.builtin(name)
}

func (m *Mapper) builtin(name string) (*Resolved, bool) {
	switch name {
	case "nvidia.com/gpu", "nvidia.com/gpu.shared":
		res := &Resolved{
			Resource:    name,
			Kind:        KindGPU,
			DeviceClass: NvidiaGPUClass,
			ClaimName:   "gpu",
			RequestName: "gpu",
			CountMode:   CountDevices,
		}
		if m.opts.DeviceClass != "" {
			res.DeviceClass = m.opts.DeviceClass
		}
		if strings.HasSuffix(name, ".shared") {
			res.Shared = true
			res.CountMode = CountSingle
			res.ClaimName = "gpu-shared"
			res.Config = m.sharingConfig("GpuConfig", false)
		}
		return res, true
	}
	if sm := migRe.FindStringSubmatch(name); sm != nil {
		profile := sm[1]
		res := &Resolved{
			Resource:    name,
			Kind:        KindMIG,
			DeviceClass: NvidiaMIGClass,
			Selectors:   []string{fmt.Sprintf("device.attributes['%s'].profile == '%s'", NvidiaDriver, profile)},
			ClaimName:   SanitizeLabel("mig-"+profile, "mig"),
			RequestName: "mig",
			CountMode:   CountDevices,
			MIGProfile:  profile,
		}
		if sm[2] != "" {
			res.Shared = true
			res.CountMode = CountSingle
			res.ClaimName = SanitizeLabel("mig-"+profile+"-shared", "mig-shared")
			res.Config = m.sharingConfig("MigDeviceConfig", true)
		}
		return res, true
	}
	return nil, false
}

// sharingConfig builds the NVIDIA driver's opaque sharing parameters. MIG
// devices only support MPS; time-slicing has no effect on them.
func (m *Mapper) sharingConfig(kind string, mig bool) *OpaqueConfig {
	var strategy string
	switch m.opts.Sharing {
	case SharingTimeSlicing:
		if mig {
			return nil
		}
		strategy = "TimeSlicing"
	case SharingMPS:
		strategy = "MPS"
	default:
		return nil
	}
	return &OpaqueConfig{
		Driver: NvidiaDriver,
		Parameters: map[string]any{
			"apiVersion": NvidiaConfigAPI,
			"kind":       kind,
			"sharing":    map[string]any{"strategy": strategy},
		},
	}
}

// Sharing returns the configured sharing strategy.
func (m *Mapper) Sharing() Sharing { return m.opts.Sharing }

var nonLabelRe = regexp.MustCompile(`[^a-z0-9]+`)

// SanitizeLabel converts s into a DNS label, returning fallback if nothing
// usable remains.
func SanitizeLabel(s, fallback string) string {
	s = nonLabelRe.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" {
		return fallback
	}
	return s
}
