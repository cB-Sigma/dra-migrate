## What this changes

<!-- One or two sentences. Link the issue if there is one. -->

## How it was verified

<!-- New or changed golden cases, commands you ran, a before/after diff. -->

## Checklist

- [ ] `make test lint` passes
- [ ] Changes to generated YAML come with a golden case in `testdata/golden/` (`make golden`, then review the diff)
- [ ] `make examples` was run if conversion output changed
- [ ] Claims about Kubernetes or driver behaviour link to the upstream source they are based on
- [ ] README / `dra-migrate explain` updated if behaviour or flags changed
