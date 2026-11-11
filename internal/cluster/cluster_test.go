package cluster

import (
	"context"
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

	"github.com/cb-Sigma/dra-migrate/internal/lint"
)

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
		t.Fatal(err)
	}
	return u
}

func listKinds(resourceVersion string) map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "nodes"}:                                           "NodeList",
		{Version: "v1", Resource: "pods"}:                                            "PodList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:                      "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}:                     "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:                       "DaemonSetList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:                      "ReplicaSetList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:                            "JobList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:                        "CronJobList",
		{Group: resourceGroup, Version: resourceVersion, Resource: "deviceclasses"}:  "DeviceClassList",
		{Group: resourceGroup, Version: resourceVersion, Resource: "resourceslices"}: "ResourceSliceList",
	}
	return m
}

func clients(t *testing.T, gitVersion, minor string, resourceVersions []string, objects ...runtime.Object) Clients {
	t.Helper()
	disc := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	disc.FakedServerVersion = &version.Info{Major: "1", Minor: minor, GitVersion: gitVersion}
	disc.Resources = []*metav1.APIResourceList{{GroupVersion: "v1"}, {GroupVersion: "apps/v1"}}
	best := "v1"
	if len(resourceVersions) > 0 {
		best = resourceVersions[0]
	}
	for _, v := range resourceVersions {
		disc.Resources = append(disc.Resources, &metav1.APIResourceList{GroupVersion: resourceGroup + "/" + v})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(best), objects...)
	return Clients{Discovery: disc, Dynamic: dyn}
}

func status(r *Readiness, name string) Status {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return ""
}

const (
	gpuClass = `apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata: {name: gpu.nvidia.com}
spec:
  extendedResourceName: nvidia.com/gpu
  selectors:
  - cel: {expression: "device.driver == 'gpu.nvidia.com'"}
`
	migClass = `apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata: {name: mig.nvidia.com}
spec: {}
`
	slice = `apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata: {name: node1-gpu.nvidia.com-abcde}
spec:
  driver: gpu.nvidia.com
  nodeName: node1
  pool: {name: node1, generation: 1, resourceSliceCount: 1}
`
	otherSlice = `apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata: {name: other}
spec:
  driver: dra.example.com
`
	plainNode = `apiVersion: v1
kind: Node
metadata: {name: node1}
status:
  allocatable: {cpu: "8"}
`
	legacyNode = `apiVersion: v1
kind: Node
metadata: {name: node2}
status:
  allocatable: {cpu: "8", nvidia.com/gpu: "4"}
`
)

func TestReadinessReady(t *testing.T) {
	c := clients(t, "v1.34.1", "34", []string{"v1", "v1beta2", "v1beta1"},
		obj(t, gpuClass), obj(t, migClass), obj(t, slice), obj(t, otherSlice), obj(t, plainNode))
	r, err := CheckReadiness(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Errorf("cluster should be ready: %+v", r.Checks)
	}
	for _, name := range []string{"kubernetes-version", "resource-api", "device-classes", "resource-slices", "device-plugin", "extended-resource-mapping"} {
		if status(r, name) != OK {
			t.Errorf("check %s = %q, want ok (%+v)", name, status(r, name), r.Checks)
		}
	}
	if strings.Join(r.ResourceAPIVersions, ",") != "v1,v1beta2,v1beta1" {
		t.Errorf("versions: %v", r.ResourceAPIVersions)
	}
	if len(r.DeviceClasses) != 2 || r.DeviceClasses[0].ExtendedResourceName != "nvidia.com/gpu" {
		t.Errorf("device classes: %+v", r.DeviceClasses)
	}
	if r.ResourceSlices["gpu.nvidia.com"] != 1 || r.ResourceSlices["dra.example.com"] != 1 || r.LegacyNodes != 0 {
		t.Errorf("slices=%v legacyNodes=%d", r.ResourceSlices, r.LegacyNodes)
	}
}

func TestReadinessProblems(t *testing.T) {
	t.Run("driver missing", func(t *testing.T) {
		c := clients(t, "v1.35.0+k3s1", "35+", []string{"v1"}, obj(t, otherSlice), obj(t, legacyNode))
		r, err := CheckReadiness(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if r.Ready() || status(r, "kubernetes-version") != OK || status(r, "device-classes") != Fail ||
			status(r, "resource-slices") != Fail || status(r, "device-plugin") != Warn || r.LegacyNodes != 1 {
			t.Errorf("unexpected checks: %+v", r.Checks)
		}
	})
	t.Run("beta API only", func(t *testing.T) {
		gpu := strings.Replace(gpuClass, "resource.k8s.io/v1", "resource.k8s.io/v1beta1", 1)
		sl := strings.Replace(slice, "resource.k8s.io/v1", "resource.k8s.io/v1beta1", 1)
		c := clients(t, "v1.32.4", "32", []string{"v1beta1"}, obj(t, gpu), obj(t, sl))
		r, err := CheckReadiness(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Ready() || status(r, "kubernetes-version") != Warn || status(r, "resource-api") != Warn || status(r, "device-classes") != OK {
			t.Errorf("unexpected checks: %+v", r.Checks)
		}
		for _, ch := range r.Checks {
			if ch.Name == "resource-api" && !strings.Contains(ch.Detail, "--api-version v1beta1") {
				t.Errorf("should recommend --api-version: %s", ch.Detail)
			}
		}
	})
	t.Run("no DRA", func(t *testing.T) {
		c := clients(t, "v1.29.0", "29", nil)
		r, err := CheckReadiness(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if r.Ready() || status(r, "kubernetes-version") != Fail || status(r, "resource-api") != Fail || len(r.Checks) != 2 {
			t.Errorf("unexpected checks: %+v", r.Checks)
		}
	})
}

const (
	deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: trainer
  namespace: ml
  managedFields: [{manager: kubectl}]
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: "2"
status:
  replicas: 1
`
	ownedReplicaSet = `apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: trainer-abc
  namespace: ml
  ownerReferences:
  - {apiVersion: apps/v1, kind: Deployment, name: trainer, uid: "1", controller: true}
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          limits:
            nvidia.com/gpu: "2"
`
	ownedPod = `apiVersion: v1
kind: Pod
metadata:
  name: trainer-abc-xyz
  namespace: ml
  ownerReferences:
  - {apiVersion: apps/v1, kind: ReplicaSet, name: trainer-abc, uid: "2", controller: true}
spec:
  containers:
  - name: main
    image: x
    resources:
      limits:
        nvidia.com/gpu: "2"
`
	barePod = `apiVersion: v1
kind: Pod
metadata:
  name: debug
  namespace: default
spec:
  containers:
  - name: shell
    image: x
    resources:
      limits:
        nvidia.com/mig-1g.5gb: "1"
`
	migratedJob = `apiVersion: batch/v1
kind: Job
metadata:
  name: done
  namespace: ml
spec:
  template:
    spec:
      containers:
      - name: main
        image: x
        resources:
          claims:
          - name: gpu
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: done-gpu
`
)

func TestScanWorkloads(t *testing.T) {
	c := clients(t, "v1.34.1", "34", []string{"v1"},
		obj(t, deployment), obj(t, ownedReplicaSet), obj(t, ownedPod), obj(t, barePod), obj(t, migratedJob))
	res, err := ScanWorkloads(context.Background(), c, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 3 || len(res.Files) != 3 {
		t.Fatalf("scanned %d workloads, want 3 (owned objects must be skipped)", res.Scanned)
	}
	findings := lint.Run(res.Files, lint.Options{NoPositions: true})
	var legacy []string
	for _, f := range findings {
		if f.RuleID == "DRA001" {
			legacy = append(legacy, f.File)
		}
		if f.Line != 0 {
			t.Errorf("cluster findings must not carry line numbers: %+v", f)
		}
	}
	if got := strings.Join(legacy, " "); got != "cluster://default/Pod/debug cluster://ml/Deployment/trainer" {
		t.Errorf("legacy workloads: %q", got)
	}
	for _, f := range res.Files {
		if strings.Contains(string(f.Data), "managedFields") || strings.Contains(string(f.Data), "status:") {
			t.Errorf("managedFields and status should be stripped:\n%s", f.Data)
		}
	}

	res, err = ScanWorkloads(context.Background(), c, "default")
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 1 || res.Files[0].Path != "cluster://default/Pod/debug" {
		t.Errorf("namespace filter: %+v", res)
	}
}

// TestReadOnly makes sure only read verbs are ever sent to the API server.
func TestReadOnly(t *testing.T) {
	c := clients(t, "v1.34.1", "34", []string{"v1"}, obj(t, gpuClass), obj(t, slice), obj(t, deployment), obj(t, plainNode))
	if _, err := CheckReadiness(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanWorkloads(context.Background(), c, ""); err != nil {
		t.Fatal(err)
	}
	dyn := c.Dynamic.(*dynamicfake.FakeDynamicClient)
	if len(dyn.Actions()) == 0 {
		t.Fatal("no API calls recorded")
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() != "list" && a.GetVerb() != "get" {
			t.Errorf("non read-only API call: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}
