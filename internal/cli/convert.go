package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cb-Sigma/dra-migrate/internal/convert"
	"github.com/cb-Sigma/dra-migrate/internal/manifest"
)

func newConvertCmd() *cobra.Command {
	var (
		mf        mapperFlags
		opts      convert.Options
		outputDir string
		inPlace   bool
		diff      bool
		quiet     bool
	)
	cmd := &cobra.Command{
		Use:   "convert [files/dirs/-]...",
		Short: "Rewrite manifests to use DRA resource claims",
		Long: `Convert replaces device-plugin extended resources with DRA resource claims.
For every workload it generates a ResourceClaimTemplate (or ResourceClaim),
adds it to spec.resourceClaims of the pod template and references it from
containers[].resources.claims. The generated object is placed in the same
file, right before the workload.

Edits are made line by line on the original text, so comments, blank lines,
key order and unrelated documents are preserved byte for byte. Converting an
already converted file changes nothing.

By default the result is written to standard output and notes to standard
error. Use --diff to preview, --in-place to rewrite files or --output-dir to
write a converted copy of every input file.`,
		Example: `  dra-migrate convert pod.yaml > pod-dra.yaml
  dra-migrate convert --diff deploy/
  dra-migrate convert --in-place deploy/
  dra-migrate convert -o out/ --api-version v1beta1 deploy/
  helm template my-chart | dra-migrate convert - > rendered-dra.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modes := 0
			for _, b := range []bool{outputDir != "", inPlace, diff} {
				if b {
					modes++
				}
			}
			if modes > 1 {
				return errors.New("--output-dir, --in-place and --diff are mutually exclusive")
			}
			m, err := mf.mapper()
			if err != nil {
				return err
			}
			opts.Mapper = m
			files, err := manifest.Load(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if inPlace {
				for _, f := range files {
					if f.Path == manifest.StdinName {
						return errors.New("--in-place cannot be used with standard input")
					}
				}
			}
			res, err := convert.Convert(files, opts)
			if err != nil {
				return err
			}
			stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

			var containers, generated, changed int
			for _, fr := range res.Files {
				containers += fr.Containers
				generated += fr.Generated
				if fr.Changed {
					changed++
				}
				if !quiet {
					for _, n := range fr.Notes {
						fmt.Fprintln(stderr, n.String())
					}
				}
			}

			switch {
			case diff:
				for _, fr := range res.Files {
					d, err := fr.UnifiedDiff()
					if err != nil {
						return err
					}
					fmt.Fprint(stdout, d)
				}
			case inPlace:
				for _, fr := range res.Files {
					if !fr.Changed {
						continue
					}
					if err := writeFile(fr.File.Path, fr.Output); err != nil {
						return err
					}
				}
			case outputDir != "":
				for _, fr := range res.Files {
					if err := writeFile(filepath.Join(outputDir, fr.File.Rel), fr.Output); err != nil {
						return err
					}
				}
			default:
				for i, fr := range res.Files {
					out := string(fr.Output)
					if i > 0 && !strings.HasPrefix(out, "---") {
						fmt.Fprintln(stdout, "---")
					}
					fmt.Fprint(stdout, out)
					if out != "" && !strings.HasSuffix(out, "\n") {
						fmt.Fprintln(stdout)
					}
				}
			}
			if !quiet {
				kind := "ResourceClaimTemplate"
				if opts.ClaimMode == convert.ModeClaim {
					kind = "ResourceClaim"
				}
				if changed == 0 {
					fmt.Fprintln(stderr, "dra-migrate: nothing to convert")
				} else {
					api := opts.APIVersion
					if api == "" {
						api = convert.APIv1
					}
					fmt.Fprintf(stderr, "dra-migrate: rewrote %d container(s) in %d file(s), generated %d %s(s) (resource.k8s.io/%s)\n",
						containers, changed, generated, kind, api)
					fmt.Fprintln(stderr, "dra-migrate: review the result before applying it; run `dra-migrate explain` for what changed and `dra-migrate cluster` to check that the cluster is ready")
				}
			}
			return nil
		},
	}
	mf.add(cmd, true)
	f := cmd.Flags()
	f.StringVar(&opts.APIVersion, "api-version", convert.APIv1, "resource.k8s.io API version to generate: v1 (Kubernetes 1.34+), v1beta2 (1.33+) or v1beta1 (1.32+)")
	f.StringVar(&opts.ClaimMode, "claim-mode", convert.ModeTemplate, "template: a ResourceClaimTemplate, every pod gets its own devices; claim: one ResourceClaim shared by all pods")
	f.BoolVar(&opts.ShareAcrossContainers, "share-across-containers", false, "let all containers of a pod that request the same resource share one claim instead of one claim per container")
	f.BoolVar(&opts.MapNodeSelectors, "map-node-selectors", false, "move nvidia.com/gpu.product, gpu.memory and gpu.family nodeSelectors into CEL selectors on the device request")
	f.StringVarP(&outputDir, "output-dir", "o", "", "write converted copies of all input files to `dir`")
	f.BoolVarP(&inPlace, "in-place", "i", false, "rewrite input files")
	f.BoolVar(&diff, "diff", false, "print a unified diff instead of the converted manifests")
	f.BoolVarP(&quiet, "quiet", "q", false, "do not print notes and the summary to standard error")
	return cmd
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}
