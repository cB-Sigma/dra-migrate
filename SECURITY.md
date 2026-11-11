# Security policy

## Supported versions

Security fixes are released for the latest minor version only.

## Reporting a vulnerability

Please do not open a public issue. Report vulnerabilities privately through
[GitHub security advisories](https://github.com/cb-Sigma/dra-migrate/security/advisories/new).
You should get an acknowledgement within a few working days. Please include
the affected version, a manifest or command that reproduces the problem, and
the impact you see.

## Scope and threat model

`dra-migrate` is a command-line tool that reads YAML and writes YAML.

* `lint` and `convert` only read the files (or standard input) you point
  them at. `convert` writes to standard output, to `--output-dir`, or, with
  `--in-place`, back to the input files. It never contacts a cluster.
* `cluster` uses your kubeconfig and sends **read-only** requests
  (discovery, `list`) to the API server. It does not create, patch or delete
  anything; this is enforced by a test.
* Manifests are treated as untrusted input. A crash, hang or excessive
  memory use caused by a crafted manifest is a bug we want to hear about.
  CEL expressions and opaque driver parameters found in a `--mapping-file`
  are copied into the generated objects verbatim: treat a mapping file like
  code and review it before use.
* Generated manifests are a proposal. Review them like any other change
  before applying them to a cluster.
