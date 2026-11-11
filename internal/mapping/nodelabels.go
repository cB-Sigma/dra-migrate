package mapping

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Node labels published by NVIDIA GPU Feature Discovery that describe the
// device rather than the node. With DRA the same constraints can be put on
// the device request itself.
const (
	LabelGPUProduct = "nvidia.com/gpu.product"
	LabelGPUMemory  = "nvidia.com/gpu.memory"
	LabelGPUFamily  = "nvidia.com/gpu.family"
)

// DeviceLabel reports whether key is a GFD label that SelectorForNodeLabel
// can translate.
func DeviceLabel(key string) bool {
	switch key {
	case LabelGPUProduct, LabelGPUMemory, LabelGPUFamily:
		return true
	}
	return false
}

var regexMeta = regexp.MustCompile(`[\\.+*?()|\[\]{}^$]`)

// SelectorForNodeLabel translates a GFD node label into a CEL device
// selector for the NVIDIA DRA driver. ok is false when the label or value
// cannot be translated.
//
// The translation is necessarily approximate: GFD normalises product names
// ("NVIDIA A100-SXM4-40GB" becomes "A100-SXM4-40GB" or
// "NVIDIA-A100-SXM4-40GB", with a "-SHARED" suffix when time-slicing is
// configured), while the DRA driver publishes the name reported by NVML.
func SelectorForNodeLabel(key, value string) (expr string, ok bool) {
	attr := fmt.Sprintf("device.attributes['%s']", NvidiaDriver)
	switch key {
	case LabelGPUProduct:
		v := strings.ToLower(strings.TrimSpace(value))
		v = strings.TrimSuffix(v, "-shared")
		v = strings.TrimPrefix(v, "nvidia-")
		if v == "" || strings.ContainsAny(v, "'\"\n") {
			return "", false
		}
		v = regexMeta.ReplaceAllStringFunc(v, func(s string) string { return `\\` + s })
		// GFD replaces whitespace with dashes, so accept either.
		v = strings.ReplaceAll(v, "-", "[ -]")
		return fmt.Sprintf("%s.productName.lowerAscii().matches('%s')", attr, v), true
	case LabelGPUMemory:
		// GFD reports memory in MiB.
		mib, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || mib <= 0 {
			return "", false
		}
		return fmt.Sprintf("device.capacity['%s'].memory.compareTo(quantity('%dMi')) >= 0", NvidiaDriver, mib), true
	case LabelGPUFamily:
		v := strings.ToLower(strings.TrimSpace(value))
		if v == "" || !regexp.MustCompile(`^[a-z0-9 _-]+$`).MatchString(v) {
			return "", false
		}
		return fmt.Sprintf("%s.architecture.lowerAscii() == '%s'", attr, v), true
	}
	return "", false
}
