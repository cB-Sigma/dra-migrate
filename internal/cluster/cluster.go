// Package cluster inspects a live cluster: it checks whether the cluster is
// ready for DRA and collects workloads for linting. It only ever issues
// read requests (discovery, get, list).
package cluster

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/mapping"
)

// Clients bundles the API clients used by this package.
type Clients struct {
	Discovery discovery.DiscoveryInterface
	Dynamic   dynamic.Interface
}

// Status of a readiness check.
type Status string

// Check statuses.
const (
	OK      Status = "ok"
	Warn    Status = "warn"
	Fail    Status = "fail"
	Unknown Status = "unknown"
)

// Check is one readiness check.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// DeviceClass summarises a DeviceClass.
type DeviceClass struct {
	Name                 string `json:"name"`
	ExtendedResourceName string `json:"extendedResourceName,omitempty"`
}

// Readiness is the result of CheckReadiness.
type Readiness struct {
	ServerVersion       string         `json:"serverVersion"`
	ResourceAPIVersions []string       `json:"resourceApiVersions"`
	DeviceClasses       []DeviceClass  `json:"deviceClasses"`
	ResourceSlices      map[string]int `json:"resourceSlicesByDriver"`
	LegacyNodes         int            `json:"nodesAdvertisingLegacyResources"`
	Checks              []Check        `json:"checks"`
}

// Ready reports whether no check failed.
func (r *Readiness) Ready() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return false
		}
	}
	return true
}

const resourceGroup = "resource.k8s.io"

// versionOrder lists resource.k8s.io versions from most to least preferred.
var versionOrder = []string{"v1", "v1beta2", "v1beta1"}

// CheckReadiness inspects the cluster's DRA prerequisites.
func CheckReadiness(ctx context.Context, c Clients) (*Readiness, error) {
	r := &Readiness{ResourceSlices: map[string]int{}}
	add := func(name string, st Status, format string, args ...any) {
		r.Checks = append(r.Checks, Check{Name: name, Status: st, Detail: fmt.Sprintf(format, args...)})
	}

	sv, err := c.Discovery.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("querying server version: %w", err)
	}
	r.ServerVersion = sv.GitVersion
	minor, _ := strconv.Atoi(strings.TrimRight(sv.Minor, "+"))
	switch {
	case sv.Major == "1" && minor >= 34:
		add("kubernetes-version", OK, "%s: DRA (resource.k8s.io/v1) is GA since v1.34", sv.GitVersion)
	case sv.Major == "1" && minor >= 32:
		add("kubernetes-version", Warn, "%s: DRA is beta and off by default before v1.34; use --api-version v1beta1 (v1beta2 from v1.33) and enable the DynamicResourceAllocation feature gate and API group", sv.GitVersion)
	default:
		add("kubernetes-version", Fail, "%s: structured-parameter DRA needs Kubernetes v1.32 or later (v1.34 for GA)", sv.GitVersion)
	}

	groups, err := c.Discovery.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discovering API groups: %w", err)
	}
	served := map[string]bool{}
	for _, g := range groups.Groups {
		if g.Name != resourceGroup {
			continue
		}
		for _, v := range g.Versions {
			served[v.Version] = true
		}
	}
	best := ""
	for _, v := range versionOrder {
		if served[v] {
			r.ResourceAPIVersions = append(r.ResourceAPIVersions, v)
			if best == "" {
				best = v
			}
		}
	}
	switch {
	case served["v1"]:
		add("resource-api", OK, "resource.k8s.io serves %s", strings.Join(r.ResourceAPIVersions, ", "))
	case best != "":
		add("resource-api", Warn, "resource.k8s.io serves only %s; generate manifests with --api-version %s", strings.Join(r.ResourceAPIVersions, ", "), best)
	default:
		add("resource-api", Fail, "the resource.k8s.io API group is not served; DRA is disabled or unsupported on this cluster")
		return r, nil
	}

	classes, err := listAll(ctx, c.Dynamic, schema.GroupVersionResource{Group: resourceGroup, Version: best, Resource: "deviceclasses"}, "")
	switch {
	case err != nil:
		add("device-classes", statusFor(err), "cannot list DeviceClasses: %v", err)
	default:
		have := map[string]bool{}
		for _, dc := range classes {
			info := DeviceClass{Name: dc.GetName()}
			info.ExtendedResourceName, _, _ = unstructured.NestedString(dc.Object, "spec", "extendedResourceName")
			r.DeviceClasses = append(r.DeviceClasses, info)
			have[info.Name] = true
		}
		sort.Slice(r.DeviceClasses, func(i, j int) bool { return r.DeviceClasses[i].Name < r.DeviceClasses[j].Name })
		if have[mapping.NvidiaGPUClass] {
			detail := fmt.Sprintf("DeviceClass %s exists", mapping.NvidiaGPUClass)
			if have[mapping.NvidiaMIGClass] {
				detail += fmt.Sprintf(" (and %s)", mapping.NvidiaMIGClass)
			}
			add("device-classes", OK, "%s", detail)
		} else {
			add("device-classes", Fail, "DeviceClass %s not found (%d DeviceClass(es) in total); install the NVIDIA DRA driver with GPU allocation enabled, or pass --device-class", mapping.NvidiaGPUClass, len(classes))
		}
		for _, dc := range r.DeviceClasses {
			if dc.ExtendedResourceName != "" {
				add("extended-resource-mapping", OK, "DeviceClass %s maps extended resource %s: with the DRAExtendedResource feature (alpha in v1.34-v1.35, beta and on by default in v1.36, GA in v1.37) unmodified pods requesting %s are served by DRA once the device plugin is removed",
					dc.Name, dc.ExtendedResourceName, dc.ExtendedResourceName)
			}
		}
	}

	slices, err := listAll(ctx, c.Dynamic, schema.GroupVersionResource{Group: resourceGroup, Version: best, Resource: "resourceslices"}, "")
	switch {
	case err != nil:
		add("resource-slices", statusFor(err), "cannot list ResourceSlices: %v", err)
	default:
		for _, s := range slices {
			driver, _, _ := unstructured.NestedString(s.Object, "spec", "driver")
			r.ResourceSlices[driver]++
		}
		if n := r.ResourceSlices[mapping.NvidiaDriver]; n > 0 {
			add("resource-slices", OK, "%d ResourceSlice(s) published by driver %s", n, mapping.NvidiaDriver)
		} else {
			add("resource-slices", Fail, "no ResourceSlices published by driver %s (%d slice(s) from other drivers); claims on its DeviceClasses cannot be allocated", mapping.NvidiaDriver, len(slices))
		}
	}

	nodes, err := listAll(ctx, c.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "")
	switch {
	case err != nil:
		add("device-plugin", Unknown, "cannot list nodes: %v", err)
	default:
		for _, n := range nodes {
			alloc, _, _ := unstructured.NestedMap(n.Object, "status", "allocatable")
			for k := range alloc {
				if strings.HasPrefix(k, mapping.NvidiaResourcePrefix) {
					r.LegacyNodes++
					break
				}
			}
		}
		if r.LegacyNodes > 0 {
			add("device-plugin", Warn, "%d node(s) advertise nvidia.com/* extended resources in status.allocatable, so a device plugin is still running there; a GPU must not be managed by the device plugin and the DRA driver at the same time", r.LegacyNodes)
		} else {
			add("device-plugin", OK, "no node advertises nvidia.com/* extended resources in status.allocatable")
		}
	}
	return r, nil
}

func statusFor(err error) Status {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return Unknown
	}
	return Fail
}

func listAll(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	var out []unstructured.Unstructured
	opts := metav1.ListOptions{Limit: 500}
	for {
		var (
			list *unstructured.UnstructuredList
			err  error
		)
		if namespace != "" {
			list, err = dyn.Resource(gvr).Namespace(namespace).List(ctx, opts)
		} else {
			list, err = dyn.Resource(gvr).List(ctx, opts)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, list.Items...)
		if list.GetContinue() == "" {
			return out, nil
		}
		opts.Continue = list.GetContinue()
	}
}

// workloadResources are scanned in this order.
var workloadResources = []struct {
	gvr  schema.GroupVersionResource
	kind string
}{
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet"},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet"},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob"},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job"},
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod"},
}

// ScanResult is the outcome of ScanWorkloads.
type ScanResult struct {
	// Files holds one pseudo file per top-level workload, named
	// "cluster://namespace/Kind/name".
	Files []*manifest.File
	// Scanned counts the top-level workloads looked at.
	Scanned int
	// Skipped lists resources that could not be listed, with the reason.
	Skipped []string
}

// ScanWorkloads lists workloads in namespace (all namespaces if empty).
// Objects managed by another object (pods of a ReplicaSet, jobs of a
// CronJob, ...) are skipped so that every workload is reported once, at
// the level where it has to be fixed.
func ScanWorkloads(ctx context.Context, c Clients, namespace string) (*ScanResult, error) {
	res := &ScanResult{}
	for _, wr := range workloadResources {
		items, err := listAll(ctx, c.Dynamic, wr.gvr, namespace)
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", wr.gvr.Resource, err))
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", wr.gvr.Resource, err)
		}
		for i := range items {
			obj := &items[i]
			if metav1.GetControllerOf(obj) != nil {
				continue
			}
			res.Scanned++
			obj.SetAPIVersion(wr.gvr.GroupVersion().String())
			obj.SetKind(wr.kind)
			obj.SetManagedFields(nil)
			unstructured.RemoveNestedField(obj.Object, "status")
			data, err := yaml.Marshal(obj.Object)
			if err != nil {
				return nil, fmt.Errorf("encoding %s %s/%s: %w", wr.kind, obj.GetNamespace(), obj.GetName(), err)
			}
			name := fmt.Sprintf("cluster://%s/%s/%s", obj.GetNamespace(), wr.kind, obj.GetName())
			res.Files = append(res.Files, manifest.Parse(name, name, data))
		}
	}
	return res, nil
}
