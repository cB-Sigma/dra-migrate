// Package explain holds the text of the built-in migration guide.
package explain

// Text is printed by "dra-migrate explain".
const Text = `FROM DEVICE PLUGINS TO DYNAMIC RESOURCE ALLOCATION

Before: a counted, opaque extended resource
-------------------------------------------
    containers:
    - name: train
      resources:
        limits:
          nvidia.com/gpu: 2

The device plugin advertises a number per node; the scheduler only counts.
Which GPU model you get, whether two containers can share it, and how it is
configured cannot be expressed in the pod.

After: a claim on a class of devices
------------------------------------
    apiVersion: resource.k8s.io/v1
    kind: ResourceClaimTemplate
    metadata:
      name: train-gpu
    spec:
      spec:
        devices:
          requests:
          - name: gpu
            exactly:
              deviceClassName: gpu.nvidia.com
              allocationMode: ExactCount
              count: 2
    ---
    spec:                             # the pod (template) spec
      containers:
      - name: train
        resources:
          claims:
          - name: gpu                 # refers to resourceClaims[].name
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: train-gpu

What dra-migrate changes
------------------------
1. The extended resource is removed from limits and requests.
2. A ResourceClaimTemplate is generated per distinct request. Kubernetes
   creates one ResourceClaim per pod from it and deletes it with the pod.
   With --claim-mode claim a single ResourceClaim is generated instead and
   every pod of the workload shares the same devices.
3. The pod spec gets a resourceClaims entry and each container that used
   the resource gets resources.claims. A container only sees devices of
   claims it references.

Resource mapping (NVIDIA DRA driver, driver name gpu.nvidia.com)
----------------------------------------------------------------
  nvidia.com/gpu: N          DeviceClass gpu.nvidia.com, count N
  nvidia.com/mig-1g.10gb: N  DeviceClass mig.nvidia.com, count N, selector
                             device.attributes['gpu.nvidia.com'].profile == '1g.10gb'
  nvidia.com/gpu.shared: N   DeviceClass gpu.nvidia.com, one device; see below
  anything else              --mapping-file

Things that do not translate one to one
---------------------------------------
* Init containers. The device plugin API takes the maximum of init and app
  container requests. A claim is allocated for the whole life of the pod, so
  an init container reuses an app container's claim when one fits.
* Shared GPUs. "nvidia.com/gpu.shared" counts replicas of a time-sliced GPU.
  DRA shares a device by letting several containers or pods reference the
  same ResourceClaim; time-slicing and MPS are per-claim driver configuration
  (--sharing) behind alpha feature gates of the NVIDIA driver.
* Node selectors. nvidia.com/gpu.product and friends select nodes; with DRA
  you select devices with CEL (--map-node-selectors). Label values and device
  attributes are produced by different components, so check the result with
  "kubectl get resourceslices -o yaml".
* Tolerations. The ExtendedResourceToleration admission plugin does not act
  on pods that use claims. Keep or add explicit tolerations.
* Quota. ResourceQuota on requests.nvidia.com/gpu no longer applies; DRA
  quota is expressed per DeviceClass
  (<device-class>.deviceclass.resource.k8s.io/devices).
* Autoscalers, schedulers and dashboards that count nvidia.com/gpu need to
  understand DRA as well.

Do you have to rewrite at all?
------------------------------
Not necessarily. A DeviceClass can declare extendedResourceName, and the
scheduler then serves ordinary "nvidia.com/gpu: N" requests from DRA devices
(feature DRAExtendedResource: alpha in Kubernetes 1.34 and 1.35, beta and on
by default in 1.36, GA in 1.37). The NVIDIA driver's gpu.nvidia.com class
sets it. Explicit claims are needed as soon as you want what extended
resources cannot express: CEL selectors, sharing a device between containers
or pods, or per-claim configuration.

Before you apply
----------------
* Kubernetes 1.34+ for resource.k8s.io/v1 (use --api-version for 1.32/1.33).
* The NVIDIA DRA driver with GPU allocation enabled, and the device plugin
  no longer managing the same GPUs. "dra-migrate cluster" checks this.
* kubectl apply --dry-run=server -f <converted manifests>
`
