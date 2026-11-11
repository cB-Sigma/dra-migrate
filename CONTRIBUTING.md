# Contributing to dra-migrate

Thanks for helping people get off device plugins. Bug reports with a
minimal manifest, new golden cases and corrections to anything this project
says about Kubernetes or a DRA driver are all very welcome.

## Getting started

You need Go 1.24 or newer and, for linting, [golangci-lint] v2.

```console
git clone https://github.com/cb-Sigma/dra-migrate.git
cd dra-migrate
make build        # bin/dra-migrate
make test         # unit + golden tests, with the race detector
make lint         # golangci-lint
```

## How the code is organised

| Path | What lives there |
|---|---|
| `cmd/dra-migrate` | `main`, nothing else |
| `internal/cli` | cobra commands, flag handling, exit codes |
| `internal/manifest` | loading files, splitting multi-document YAML, finding pod specs and legacy requests in `yaml.v3` node trees |
| `internal/mapping` | resource name → DeviceClass/selector rules, the mapping file format, GFD label → CEL translation |
| `internal/lint` | lint rules and findings |
| `internal/report` | text, JSON, SARIF and GitHub annotation output |
| `internal/convert` | planning claims, mutating the node tree, patching the source text, rendering claim objects, diffs |
| `internal/cluster` | read-only cluster scan and readiness checks |
| `internal/verify` | test helper: strict decoding into `k8s.io/api` types, CEL compilation |
| `testdata/golden` | input → expected output cases |
| `examples` | `before/` and tool-generated `after/` |

### How conversion preserves formatting

`convert` never re-serialises a document it can avoid re-serialising:

1. The document is parsed into a `yaml.v3` node tree. All decisions are made
   on that tree, and the planned edits are recorded as a list of small
   operations (remove this key, append this pair, append this item).
2. The operations are applied to the tree **and**, independently, to the
   original text as line deletions/insertions (`internal/convert/patch.go`).
3. The patched text is parsed again and compared with the mutated tree. Only
   if both agree is the patched text used. Otherwise (flow style, tabs,
   exotic layouts) the document is re-encoded from the tree and a note is
   printed.

If you touch the patcher, keep step 3 intact: it is what makes a layout we
did not think of degrade into "formatting changed" instead of "wrong YAML".

## Tests

The quality bar for generated YAML is that it is **schema-valid by
construction of the test suite**, not by inspection:

* **Golden tests** (`internal/cli/golden_test.go`). Every directory in
  `testdata/golden/` has an `input.yaml`, optional CLI flags in `args`, and
  the expected `expected.yaml` / `expected.notes`. For each case the test
  also strictly decodes every output document into the real Go type from
  `k8s.io/api` (unknown fields are errors), compiles every CEL selector with
  the scheduler's DRA CEL environment, resolves all claim references,
  converts the output a second time (idempotence) and lints it.
* **Adding a case**: create the directory with `input.yaml` (and `args`),
  run `make golden`, and **read the generated files** before committing.
* **Examples**: `examples/after` is generated (`make examples`); a test
  fails if it is stale. The diff at the top of the README is checked against
  real tool output as well.
* **API server dry-run**: `make dry-run` starts a throwaway `kube-apiserver`
  and `etcd` (envtest binaries, no containers needed) and runs
  `kubectl apply --dry-run=server` on every converted fixture. CI does this
  for the oldest and newest Kubernetes release we support. Use
  `K8S_VERSION=1.37.0 make dry-run` to pick a release.

## Claims about upstream behaviour

DRA and its drivers move quickly. If a change relies on how Kubernetes or a
driver behaves (a field name, a DeviceClass, an attribute, a feature gate's
maturity), link the upstream source in the pull request and, where users see
the claim, say which versions it applies to. "Not verified" is an acceptable
thing to write in the README; a guess presented as fact is not.

## Pull requests

* Keep changes focused; unrelated refactoring makes review slower.
* Commit messages: imperative mood, a short subject, and the *why* in the
  body when it is not obvious.
* New flags and rules need documentation in the README and, if they change
  what users should do, in `dra-migrate explain`.
* By contributing you agree that your contribution is licensed under the
  Apache License 2.0.

## Releasing (maintainers)

Tag `vX.Y.Z` on `main` and push the tag. The release workflow runs
GoReleaser, which publishes binaries, checksums and the container image.

[golangci-lint]: https://golangci-lint.run/
