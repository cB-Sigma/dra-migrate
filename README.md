# dra-migrate

**Find every `nvidia.com/gpu` in your Kubernetes manifests and rewrite it to Dynamic Resource Allocation, without reformatting a single line you did not ask it to touch.**

[![CI](https://github.com/cb-Sigma/dra-migrate/actions/workflows/ci.yml/badge.svg)](https://github.com/cb-Sigma/dra-migrate/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/cb-Sigma/dra-migrate)](https://goreportcard.com/report/github.com/cb-Sigma/dra-migrate)
[![Go version](https://img.shields.io/github/go-mod/go-version/cb-Sigma/dra-migrate)](go.mod)
[![License](https://img.shields.io/github/license/cb-Sigma/dra-migrate)](LICENSE)

`dra-migrate` is a linter and converter for the move from device plugins
(`resources.limits["nvidia.com/gpu"]`) to
[Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
(`ResourceClaimTemplate` + `spec.resourceClaims` + `resources.claims`).

```console
$ dra-migrate convert --diff examples/before/02-deployment-multi-gpu.yaml
```

<!-- BEGIN DIFF: output of the command above, kept in sync by TestReadmeDiff -->
```diff
--- a/examples/before/02-deployment-multi-gpu.yaml
+++ b/examples/before/02-deployment-multi-gpu.yaml
@@ -1,3 +1,18 @@
+apiVersion: resource.k8s.io/v1
+kind: ResourceClaimTemplate
+metadata:
+  name: trainer-gpu
+  namespace: ml
+spec:
+  spec:
+    devices:
+      requests:
+        - name: gpu
+          exactly:
+            deviceClassName: gpu.nvidia.com
+            allocationMode: ExactCount
+            count: 4
+---
 apiVersion: apps/v1
 kind: Deployment
 metadata:
@@ -26,7 +41,8 @@
             limits:
               cpu: "8"
               memory: 64Gi
-              nvidia.com/gpu: 4 # one node's worth
+            claims:
+              - name: gpu # one node's worth
           volumeMounts:
             - name: data
               mountPath: /data
@@ -34,3 +50,6 @@
         - name: data
           persistentVolumeClaim:
             claimName: training-data
+      resourceClaims:
+        - name: gpu
+          resourceClaimTemplateName: trainer-gpu
```
<!-- END DIFF -->

Comments move with the line they belong to, the file's own indentation
style is followed, and everything else is byte-for-byte what you wrote. Run
it twice and nothing changes.

## Contents

- [Why now](#why-now)
- [Install](#install)
- [Quick start](#quick-start)
- [What gets converted](#what-gets-converted)
- [What lint reports](#what-lint-reports)
- [What it deliberately does not do](#what-it-deliberately-does-not-do)
- [Use it in CI](#use-it-in-ci)
- [Command reference](#command-reference)
- [Mapping file](#mapping-file)
- [Compatibility](#compatibility)
- [How it works, and how it is tested](#how-it-works-and-how-it-is-tested)
- [FAQ](#faq)
- [Roadmap](#roadmap)

## Why now

- **DRA is GA.** `resource.k8s.io/v1` has been stable and on by default
  since Kubernetes 1.34; the feature gate is locked on since 1.35. Every
  Kubernetes release that is still supported upstream ships it.
- **The device plugin API cannot say what you mean.** An extended resource
  is an opaque integer per node. "Any A100", "at least 40 GiB", "these two
  containers share one GPU", "this GPU, time-sliced" are all impossible to
  express in the pod, so they ended up in node labels, per-node plugin
  configuration and tribal knowledge.
- **DRA can.** A claim names a `DeviceClass`, filters devices with CEL,
  carries per-claim driver configuration, and can be shared by containers
  or pods.
- **The rewrite is mechanical but easy to get subtly wrong**: the shape of
  the request differs between `v1beta1` and `v1`, init containers behave
  differently, tolerations stop being injected, and `kubectl` will happily
  reformat your carefully commented manifests. That is the part this tool
  takes off your hands, and the rest is what it warns you about.

You may not need to rewrite anything on day one: see
[Do I have to migrate at all?](#do-i-have-to-migrate-at-all)

## Install

**Go**

```console
go install github.com/cb-Sigma/dra-migrate/cmd/dra-migrate@latest
```

**From source**

```console
git clone https://github.com/cb-Sigma/dra-migrate.git
cd dra-migrate
make build          # ./bin/dra-migrate, a static binary
```

**Release binaries** for Linux, macOS and Windows (amd64/arm64) are attached
to each [GitHub release](https://github.com/cb-Sigma/dra-migrate/releases)
together with `checksums.txt`.

**Container image**

```console
docker run --rm -v "$PWD:/work" ghcr.io/cb-sigma/dra-migrate lint .
```

**kubectl plugin** (optional): put the binary on your `PATH` under the name
`kubectl-dra_migrate` and run `kubectl dra-migrate cluster`.

## Quick start

```console
# 1. What is still using device-plugin requests? (exit status 1 if anything is)
dra-migrate lint deploy/

# 2. Preview the migration
dra-migrate convert --diff deploy/

# 3. Rewrite the files, then review the diff like any other change
dra-migrate convert --in-place deploy/

# 4. Is the cluster ready, and what is still running the old way? (read-only)
dra-migrate cluster

# 5. Let the API server have the last word
kubectl apply --dry-run=server -f deploy/
```

Helm charts: render first, the tool does not parse Go templates.

```console
helm template my-release ./chart | dra-migrate lint -
helm template my-release ./chart | dra-migrate convert --diff -
```

Unrendered templates are detected, reported as `DRA900` and left alone
rather than half-parsed. Port the change back into the chart by hand; the
diff of the rendered output tells you exactly what to change.

`lint` output looks like this (real output for [`examples/before`](examples/before), shortened):

```text
examples/before/02-deployment-multi-gpu.yaml:29:15: error DRA001 Deployment/ml/trainer: container "trainer": nvidia.com/gpu: 4 (limits) is a device-plugin request; replace it with a DRA claim on DeviceClass gpu.nvidia.com [fixable]
examples/before/03-job-init-container.yaml:17:13: warning DRA007 Job/finetune: init container "gpu-check" requests nvidia.com/gpu; DRA claims are held for the whole pod lifetime, review how the claim is shared with app containers
examples/before/06-deployment-mig.yaml:20:13: error DRA001 Deployment/notebook: container "jupyter": nvidia.com/mig-1g.10gb: 1 (limits) is a device-plugin request; replace it with a DRA claim on DeviceClass mig.nvidia.com with a CEL selector [fixable]

9 error(s), 1 warning(s), 5 note(s); 9 fixable with `dra-migrate convert`.
```

`dra-migrate explain` prints a one-page guide to what changes and why.

## What gets converted

Workloads: `Pod`, `Deployment`, `StatefulSet`, `DaemonSet`, `ReplicaSet`,
`ReplicationController`, `Job`, `CronJob`, `PodTemplate`, and any custom
resource that embeds a pod spec (found structurally: a mapping with a
`containers` list; think Kubeflow training jobs, KubeRay, Argo Rollouts,
JobSet, Volcano). For custom resources a warning asks you to check that the
CRD schema keeps `resourceClaims`.

| Legacy request | Generated device request | Notes |
|---|---|---|
| `nvidia.com/gpu: N` | DeviceClass `gpu.nvidia.com`, `count: N` | `--device-class` overrides the class |
| `nvidia.com/mig-<profile>: N` (for example `mig-1g.10gb`) | DeviceClass `mig.nvidia.com`, `count: N`, selector `device.attributes['gpu.nvidia.com'].profile == '<profile>'` | Nodes must publish matching MIG devices |
| `nvidia.com/gpu.shared: N`, `nvidia.com/mig-<profile>.shared: N` | Same class, **one** device, optional `GpuConfig`/`MigDeviceConfig` sharing block (`--sharing`) | Semantics change, see below; a warning is always printed |
| anything in `--mapping-file` | whatever you map it to | [Mapping file](#mapping-file) |

How requests become claims:

| Situation | Result |
|---|---|
| One container, `N` GPUs | One claim with `allocationMode: ExactCount`, `count: N` (omitted for `N = 1`, where it is the API default) |
| Several containers with GPUs | One claim **per container**, like the device plugin gave each its own devices. Identical requests share one `ResourceClaimTemplate` object. `--share-across-containers` gives them one common claim instead |
| Init container with GPUs | Reuses the claim of an app container that requests at least as many devices; otherwise it gets its own claim and a warning. Sidecars (`restartPolicy: Always`) count as app containers |
| Set in both `requests` and `limits` | Both entries are removed; the limit wins if they disagree (which the API server would have rejected anyway) |
| `nvidia.com/gpu: 0` | Left untouched |
| `--claim-mode claim` | A `ResourceClaim` instead of a template: every pod of the workload shares the same device(s) |
| `--map-node-selectors` | `nodeSelector` entries for `nvidia.com/gpu.product`, `gpu.memory` and `gpu.family` move into CEL selectors on the request |
| `--api-version v1beta1` | Same request without the `exactly:` wrapper that `v1beta2` and `v1` use |

Generated objects are named `<workload>-<claim>` (for example `trainer-gpu`),
carry the workload's namespace, and are written into the same file right
before the workload. Names never collide with objects already in your
input; an identical existing object is reused instead of duplicated.

## What lint reports

| Rule | Severity | Meaning |
|---|---|---|
| `DRA001` legacy-device-request | error | A container requests a mapped extended resource. Fixable by `convert` |
| `DRA002` requests-limits-mismatch | error | Request without limit, or request ≠ limit, for an extended resource |
| `DRA003` unparsable-quantity | warning | Quantity is not a whole number of devices |
| `DRA004` zero-quantity | info | `nvidia.com/gpu: 0` |
| `DRA005` unmapped-resource | warning | `nvidia.com/*` resource without a known mapping |
| `DRA006` shared-resource | warning | `.shared` (time-sliced/MPS) resource: meaning changes under DRA |
| `DRA007` init-container-request | warning | Init container requests a device |
| `DRA101` gpu-node-selector | info | `nodeSelector` on an `nvidia.com/*` label; shows the equivalent CEL selector where one exists |
| `DRA102` gpu-node-affinity | info | Node affinity on an `nvidia.com/*` label (never rewritten) |
| `DRA103` nvidia-runtime-class | info | `runtimeClassName: nvidia` may no longer be needed |
| `DRA104` no-gpu-toleration | info | No toleration for the `nvidia.com/gpu` taint |
| `DRA105` visible-devices-env | warning | `NVIDIA_VISIBLE_DEVICES` is set by hand |
| `DRA106` custom-resource-pod-template | info | Pod template found inside a custom resource |
| `DRA900` helm-template | info | Unrendered Helm template skipped |
| `DRA901` parse-error | error | YAML does not parse |

The 1xx rules are only evaluated for workloads that still have a legacy
request, so a migrated repository lints clean. Exit status: `0` nothing at or
above `--fail-on` (default `warning`), `1` findings, `2` usage or I/O error.

## What it deliberately does not do

`dra-migrate` rewrites manifests. It does not make decisions that need
knowledge of your cluster, and it tells you when it meets one.

- **It does not install or configure anything.** You need a DRA driver with
  published `ResourceSlice`s and the `DeviceClass` the claims refer to.
  `dra-migrate cluster` checks, it never changes.
- **It does not parse Helm templates, Kustomize patches or Jsonnet.** Work
  on rendered output and port the change back.
- **Shared GPUs are not translated one to one, because they cannot be.**
  `nvidia.com/gpu.shared: 1` means "one replica of a time-sliced GPU". DRA
  has no replicas: pods share a device by referencing the *same*
  `ResourceClaim`. The default output (a template) gives every pod its own
  GPU; `--claim-mode claim` shares one. Which is right depends on what you
  were using time-slicing for.
- **Node affinity is never rewritten**, and `nodeSelector` only with
  `--map-node-selectors`. GFD label values and DRA device attributes come
  from different components (`A100-SXM4-40GB` vs. `NVIDIA A100-SXM4-40GB`);
  the generated selectors are tolerant, but compare them with
  `kubectl get resourceslices -o yaml` before relying on them.
- **Tolerations are not added.** If you rely on the
  `ExtendedResourceToleration` admission plugin (some managed platforms do),
  pods that use claims no longer get the toleration injected. `DRA104`
  points at the affected workloads.
- **`runtimeClassName` and `NVIDIA_VISIBLE_DEVICES` are reported, not
  removed.**
- **Quota, autoscaling and scheduling integrations are out of scope.**
  `ResourceQuota` on `requests.nvidia.com/gpu` stops applying to converted
  workloads (DRA quota is per DeviceClass), and anything that counts
  `nvidia.com/gpu` (cluster autoscaler configuration, queueing systems,
  dashboards, chargeback) has to learn about claims.
- **Bare pods cannot be updated in place.** Container resources of an
  existing pod are immutable: delete and recreate. Workload controllers roll
  the change out like any other pod template change.
- **JSON manifests, `kind: List` wrappers and pod templates that use YAML
  anchors are linted but not rewritten.**
- **Only NVIDIA resources are built in.** Other vendors work through a
  [mapping file](#mapping-file).

## Use it in CI

### GitHub Actions

Annotate pull requests and fail when a device-plugin request sneaks back in:

```yaml
name: dra-lint
on: [pull_request]
jobs:
  dra-migrate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: stable
      - run: go install github.com/cb-Sigma/dra-migrate/cmd/dra-migrate@latest
      - run: dra-migrate lint --format github deploy/
```

Or upload SARIF to GitHub code scanning:

```yaml
      - run: dra-migrate lint --format sarif --fail-on none deploy/ > dra-migrate.sarif
      - uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: dra-migrate.sarif
```

Charts:

```yaml
      - run: helm template ci ./chart | dra-migrate lint --format github -
```

Findings from standard input are reported against `<stdin>`, so prefer text
output there; annotations need real file paths.

### pre-commit

```yaml
repos:
  - repo: https://github.com/cb-Sigma/dra-migrate
    rev: v0.1.0   # use the latest release tag
    hooks:
      - id: dra-migrate-lint
      # or rewrite automatically:
      # - id: dra-migrate-convert
```

The hooks build the binary with Go (`language: golang`), so no separate
install is needed.

### Anything else

`--format json` is stable, line-oriented `--format text` is grep-friendly,
and the exit status does the rest.

## Command reference

```text
dra-migrate lint     [files/dirs/-]...   report legacy requests
dra-migrate convert  [files/dirs/-]...   rewrite manifests
dra-migrate cluster                      scan a live cluster, check readiness (read-only)
dra-migrate explain                      what changes and why
dra-migrate version
```

Arguments are files, directories (searched recursively for `.yaml`, `.yml`
and `.json`; hidden directories are skipped) or `-` for standard input.

### `lint`

| Flag | Default | |
|---|---|---|
| `-f`, `--format` | `text` | `text`, `json`, `sarif` or `github` |
| `--fail-on` | `warning` | Lowest severity that makes the exit status 1: `error`, `warning`, `info`, `none` |
| `--mapping-file` | | Custom mappings; resources in it are linted too |

### `convert`

| Flag | Default | |
|---|---|---|
| *(none)* | | Write converted manifests to stdout, notes to stderr |
| `--diff` | | Print a unified diff, change nothing |
| `-i`, `--in-place` | | Rewrite the input files (only those that change) |
| `-o`, `--output-dir` | | Write a converted copy of every input file, mirroring directories |
| `--api-version` | `v1` | `v1`, `v1beta2` or `v1beta1` of `resource.k8s.io` |
| `--claim-mode` | `template` | `template` (per-pod claims) or `claim` (one shared `ResourceClaim`) |
| `--share-across-containers` | `false` | One claim per resource per pod instead of one per container |
| `--map-node-selectors` | `false` | Turn GFD device labels in `nodeSelector` into CEL selectors |
| `--device-class` | `gpu.nvidia.com` | DeviceClass for full GPUs |
| `--sharing` | `none` | For `.shared` resources: `time-slicing` or `mps` adds the NVIDIA driver's opaque sharing config |
| `--mapping-file` | | Custom mappings |
| `-q`, `--quiet` | | No notes or summary on stderr |

### `cluster`

Uses your kubeconfig (`--kubeconfig`, `--context`, `-n/--namespace`). It

1. checks readiness: server version, served `resource.k8s.io` versions,
   presence of the `gpu.nvidia.com` DeviceClass, `ResourceSlice`s published
   by the driver, DeviceClasses with `extendedResourceName`, and nodes that
   still advertise `nvidia.com/*` through a device plugin;
2. lints live Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs,
   CronJobs and bare Pods. Objects owned by a controller are skipped so each
   workload shows up once, where it has to be fixed.

It only sends discovery and `list` requests. Output formats and `--fail-on`
work as for `lint`; `--skip-readiness` and `--skip-workloads` run one half.

```text
$ dra-migrate cluster
DRA readiness (server v1.34.1)
  [ok     ] kubernetes-version: v1.34.1: DRA (resource.k8s.io/v1) is GA since v1.34
  [ok     ] resource-api: resource.k8s.io serves v1
  [ok     ] device-classes: DeviceClass gpu.nvidia.com exists
  [ok     ] resource-slices: 1 ResourceSlice(s) published by driver gpu.nvidia.com
  [ok     ] device-plugin: no node advertises nvidia.com/* extended resources in status.allocatable
  ResourceSlices by driver: gpu.nvidia.com=1

Workloads (2 scanned)
cluster://ml/Deployment/trainer: error DRA001 Deployment/ml/trainer: container "trainer": nvidia.com/gpu: 4 (limits) is a device-plugin request; replace it with a DRA claim on DeviceClass gpu.nvidia.com [fixable]
```

## Mapping file

Teach the tool about other resources, or override the built-in rules.
Rules are tried in order; the first match wins, then the built-ins.

```yaml
mappings:
- resource: gpu.intel.com/i915          # exact name, or a glob such as example.com/fpga-*
  deviceClass: gpu.intel.com            # required
  claimName: igpu                       # base name for claims (default: derived from the resource)
  requestName: gpu                      # request name inside the claim (default: claimName)
  countMode: devices                    # devices (default) or single
  selectors:                            # CEL, one selector each
  - device.capacity['gpu.intel.com'].memory.compareTo(quantity('8Gi')) >= 0
  config:                               # optional opaque driver parameters
    driver: gpu.intel.com
    parameters:
      apiVersion: gpu.intel.com/v1
      kind: GpuConfig
  notes:
  - printed whenever this rule is applied
```

Unknown fields are rejected. A complete example is in
[`examples/mapping.yaml`](examples/mapping.yaml). The names in a mapping file
are yours to get right: the tool cannot know another vendor's DeviceClasses
or attributes.

## Compatibility

Everything in these tables was checked against the linked upstream sources
in November 2026. DRA drivers move fast: check again before you plan a
production migration.

### Kubernetes

| Kubernetes | `--api-version` | Notes |
|---|---|---|
| 1.34 and later | `v1` (default) | `resource.k8s.io/v1` is served by default. Beta versions are only served if the cluster enables them with `--runtime-config` |
| 1.33 | `v1beta2` | DRA is beta and off by default: needs the `DynamicResourceAllocation` feature gate and the API group enabled |
| 1.32 | `v1beta1` | As above. `v1beta1` requests have no `exactly:` wrapper |
| 1.31 and earlier | not supported | Different (alpha) API |

- A v1.37.0 API server reports `v1beta1` as "deprecated in v1.35+,
  unavailable in v1.38+" and `v1beta2` as "deprecated in v1.36+, unavailable
  in v1.39+". Generate `v1` unless you must support an old cluster.
- Output for all three versions is validated by server-side dry-run against
  kube-apiserver 1.34.1 and 1.37.0 in CI. Clusters older than 1.34 are **not**
  part of that test; `v1beta1`/`v1beta2` output is verified by strict
  decoding into the `k8s.io/api` types and by the beta APIs of those two
  servers only.

### NVIDIA DRA driver

The driver lives at
[kubernetes-sigs/dra-driver-nvidia-gpu](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu)
(formerly `NVIDIA/k8s-dra-driver-gpu`, which redirects there). Verified
against its documentation for v0.5.1:

| What `dra-migrate` relies on | Status in the driver |
|---|---|
| Driver name `gpu.nvidia.com`; DeviceClasses `gpu.nvidia.com` (full GPUs) and `mig.nvidia.com` (MIG devices) | Shipped by the Helm chart when GPU allocation is enabled |
| Attributes `productName`, `architecture`, `profile` (MIG), capacity `memory` | Published in `ResourceSlice`s (see the driver's "ResourceSlice device attributes" reference) |
| **GPU allocation itself** | The upstream README calls it "not yet officially supported"; the chart only enables it with `gpuResourcesEnabledOverride=true`. Requires Kubernetes 1.34.2 or later |
| Static MIG (pre-created partitions) | Enabled by default |
| Dynamic MIG | Feature gate `DynamicMIG`, **alpha**, off by default |
| Time-slicing configuration (`--sharing time-slicing`) | Feature gate `TimeSlicingSettings`, **alpha**, off by default. Has no effect on MIG devices, so the tool never emits it for them |
| MPS (`--sharing mps`) | Feature gate `MPSSupport`, **alpha**, off by default; mutually exclusive with `DynamicMIG` |
| `extendedResourceName: nvidia.com/gpu` on the `gpu.nvidia.com` class | Set by the chart for `resource.k8s.io/v1` |

Because sharing is alpha and version-dependent, `--sharing` defaults to
`none` and the tool prints a warning whenever it converts a `.shared`
resource. The opaque config it can emit
(`apiVersion: resource.nvidia.com/v1beta1`, `kind: GpuConfig` /
`MigDeviceConfig`, `sharing.strategy: TimeSlicing|MPS`) matches the driver's
Go API types; it has **not** been exercised on real GPUs by this project.

Not verified, and therefore not claimed: behaviour of drivers older than
v0.5; any cloud provider's managed DRA offering; other vendors' drivers.

## How it works, and how it is tested

**Format-preserving edits.** Each document is parsed into a `yaml.v3` node
tree, on which all decisions are made. The resulting edits are applied to
the tree and, separately, to the original text as whole-line insertions and
deletions. The patched text is then parsed again and compared with the
edited tree; only if they agree is it used. Documents the line patcher does
not understand (flow style such as `resources: {limits: {...}}`, tabs) are
re-encoded from the tree instead, and you are told. Documents without
changes are copied through verbatim.

**Schema validity is tested, not assumed.** In the test suite every
generated object is

- decoded **strictly** (unknown and duplicate fields are errors) into the
  real Go types from `k8s.io/api`: `resource/v1`, `v1beta1`, `v1beta2`,
  `core/v1`, `apps/v1`, `batch/v1`;
- checked for dangling references between containers, pod claims and
  generated objects;
- has its CEL selectors compiled with the scheduler's DRA CEL environment
  (`k8s.io/dynamic-resource-allocation/cel`) and evaluated against sample
  devices modelled on the driver's documentation;
- converted a second time to prove idempotence, and linted to prove nothing
  fixable is left.

23 golden cases in [`testdata/golden`](testdata/golden) cover single and
multiple GPUs, several containers, init containers and sidecars, MIG,
CronJobs, multi-document files with comments, already migrated input,
requests + limits, shared resources, node selectors, custom resources, all
API versions, mapping files and rendered Helm output.

**A real API server has the last word.** `make dry-run` starts a throwaway
`kube-apiserver` + `etcd` and runs `kubectl apply --dry-run=server` on every
converted fixture; CI does so for Kubernetes 1.34.1 and 1.37.0. That covers
API validation, including the server's own CEL compilation.

**What is not tested:** scheduling and running pods on real GPUs. There is
no GPU in CI. Whether a converted workload gets the device you expect is a
property of your cluster and driver, which is why the tool keeps telling you
to review its output.

## FAQ

### Do I have to migrate at all?

Maybe not yet. A `DeviceClass` can declare
`extendedResourceName: nvidia.com/gpu`, and the scheduler then satisfies
plain `nvidia.com/gpu: N` requests from DRA devices without any change to
the pod. That feature (`DRAExtendedResource`) is alpha in Kubernetes 1.34
and 1.35, beta and on by default in 1.36, and GA in 1.37. The NVIDIA
driver's `gpu.nvidia.com` class sets it; for MIG you create one DeviceClass
per profile yourself, and the device plugin must not be serving the same
resource on the same node.

Explicit claims are what you need for everything extended resources cannot
express: CEL selectors, sharing a device between containers or pods,
per-claim configuration, several kinds of device in one request. A common
path is: switch the driver first and keep manifests as they are, then use
`dra-migrate` on the workloads that benefit.

### Does it touch my cluster?

`lint` and `convert` never open a network connection. `cluster` only reads
(a test asserts that only `list`/`get` verbs are used).

### Why does a GPU request in an init container not get its own claim?

The device plugin API computes a pod's need as the maximum of its init
containers and the sum of its app containers, so the init container borrows
a GPU that an app container uses later. A DRA claim is allocated for the
whole lifetime of the pod; a separate claim would pin an extra GPU.
Referencing the same claim from both containers is the faithful translation.

### Can I mix both styles in one cluster during the migration?

Per node and resource, a GPU should be managed by either the device plugin
or the DRA driver, not both. Across nodes both can coexist, and
`dra-migrate cluster` shows which nodes still advertise device-plugin
resources. Follow your driver's installation guide for the supported
combinations.

### The diff reformatted my file. Why?

It should not: that only happens for documents written in flow style or
with tabs, and a note says so. If a normally formatted document is
re-serialised, please [open an issue](https://github.com/cb-Sigma/dra-migrate/issues/new/choose)
with the manifest.

### Why is `count` missing for a single GPU?

`allocationMode: ExactCount` with `count: 1` is the API default, and the
upstream examples omit it too.

### Does it support AMD, Intel or other accelerators?

Through a [mapping file](#mapping-file). Built-in rules for other vendors
are welcome once someone has verified the DeviceClass and attribute names
against the driver.

## Roadmap

Planned, in rough order. None of this exists yet.

- `convert --extended-resource`: emit DeviceClasses with
  `extendedResourceName` (one per MIG profile) for the "keep the manifests"
  path
- Prioritized alternatives (`firstAvailable`) for "A100, else any GPU" style
  requests
- Readiness checks for more drivers and for driver feature gates
- Kustomize awareness (patches and `replacements` that touch GPU limits)
- Built-in mappings for other vendors' drivers, verified against real
  clusters
- Rewriting `kind: List` wrappers and JSON input
- A Krew manifest

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The most valuable contributions are
manifests that convert badly and corrections to the compatibility notes
above, with a link to the upstream source.

## License

[Apache License 2.0](LICENSE). Copyright The dra-migrate Authors.

Not affiliated with or endorsed by NVIDIA or the Kubernetes project.
