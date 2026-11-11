package convert

import (
	"sort"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/cb-Sigma/dra-migrate/internal/mapping"
	"github.com/cb-Sigma/dra-migrate/internal/yamlutil"
)

// Supported resource.k8s.io API versions.
const (
	APIv1      = "v1"
	APIv1beta1 = "v1beta1"
	APIv1beta2 = "v1beta2"
	apiGroup   = "resource.k8s.io"
)

// Claim object kinds.
const (
	KindTemplate = "ResourceClaimTemplate"
	KindClaim    = "ResourceClaim"
)

// deviceRequest is everything needed to render one claim.
type deviceRequest struct {
	res       *mapping.Resolved
	count     int64
	selectors []string
}

// claimSpec builds the ResourceClaimSpec node ("devices: ...") for the
// given API version.
//
// resource.k8s.io/v1 and v1beta2 nest the request under "exactly";
// v1beta1 has the same fields directly on the request.
func claimSpec(apiVersion string, r deviceRequest) *yaml.Node {
	body := yamlutil.Map("deviceClassName", r.res.DeviceClass)
	if r.count > 1 {
		yamlutil.Append(body, "allocationMode", yamlutil.Scalar("ExactCount"))
		yamlutil.Append(body, "count", yamlutil.Int(strconv.FormatInt(r.count, 10)))
	}
	if len(r.selectors) > 0 {
		sels := yamlutil.Seq()
		for _, s := range r.selectors {
			sels.Content = append(sels.Content, yamlutil.Map("cel", yamlutil.Map("expression", s)))
		}
		yamlutil.Append(body, "selectors", sels)
	}
	var req *yaml.Node
	if apiVersion == APIv1beta1 {
		req = yamlutil.Map("name", r.res.RequestName)
		req.Content = append(req.Content, body.Content...)
	} else {
		req = yamlutil.Map("name", r.res.RequestName, "exactly", body)
	}
	devices := yamlutil.Map("requests", yamlutil.Seq(req))
	if c := r.res.Config; c != nil {
		params, err := yamlutil.FromAny(sortedParams(c.Parameters))
		if err == nil {
			cfg := yamlutil.Map(
				"requests", yamlutil.Seq(yamlutil.Scalar(r.res.RequestName)),
				"opaque", yamlutil.Map("driver", c.Driver, "parameters", params),
			)
			yamlutil.Append(devices, "config", yamlutil.Seq(cfg))
		}
	}
	return yamlutil.Map("devices", devices)
}

// sortedParams orders opaque parameters with apiVersion and kind first and
// everything else alphabetically, so output is deterministic.
func sortedParams(p map[string]any) *yaml.Node {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		switch k {
		case "apiVersion":
			return 0
		case "kind":
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	m := yamlutil.Map()
	for _, k := range keys {
		var v *yaml.Node
		if sub, ok := p[k].(map[string]any); ok {
			v = sortedParams(sub)
		} else {
			n, err := yamlutil.FromAny(p[k])
			if err != nil {
				continue
			}
			v = n
		}
		yamlutil.Append(m, k, v)
	}
	return m
}

// claimObject builds a complete ResourceClaimTemplate or ResourceClaim.
func claimObject(apiVersion, kind, name, namespace string, spec *yaml.Node) *yaml.Node {
	meta := yamlutil.Map("name", name)
	if namespace != "" {
		yamlutil.Append(meta, "namespace", yamlutil.Scalar(namespace))
	}
	if kind == KindTemplate {
		spec = yamlutil.Map("spec", spec)
	}
	return yamlutil.Map(
		"apiVersion", apiGroup+"/"+apiVersion,
		"kind", kind,
		"metadata", meta,
		"spec", spec,
	)
}
