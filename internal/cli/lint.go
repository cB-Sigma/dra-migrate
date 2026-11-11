package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cb-Sigma/dra-migrate/internal/lint"
	"github.com/cb-Sigma/dra-migrate/internal/manifest"
	"github.com/cb-Sigma/dra-migrate/internal/report"
	"github.com/cb-Sigma/dra-migrate/internal/version"
)

func newLintCmd() *cobra.Command {
	var (
		mf     mapperFlags
		format string
		failOn string
	)
	cmd := &cobra.Command{
		Use:   "lint [files/dirs/-]...",
		Short: "Report legacy device-plugin GPU requests in manifests",
		Long: `Lint scans YAML manifests for containers that request devices through
device-plugin extended resources (nvidia.com/gpu, nvidia.com/mig-*,
nvidia.com/gpu.shared, or anything in --mapping-file) and for related settings
that need attention after a migration to DRA.

Arguments are files, directories (searched recursively for .yaml, .yml and
.json) or "-" for standard input. Unrendered Helm templates are detected and
skipped; pipe "helm template" output instead.

Exit status is 1 when a finding at or above --fail-on is reported, 2 on
usage or I/O errors, 0 otherwise.`,
		Example: `  dra-migrate lint deploy/
  dra-migrate lint --format github deploy/      # GitHub Actions annotations
  dra-migrate lint --format sarif . > dra.sarif
  helm template my-chart | dra-migrate lint -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			threshold, err := lint.ParseSeverity(failOn)
			if err != nil {
				return err
			}
			if !validFormat(format) {
				return fmt.Errorf("unknown --format %q (want one of %s)", format, strings.Join(report.Formats, ", "))
			}
			m, err := mf.mapper()
			if err != nil {
				return err
			}
			files, err := manifest.Load(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			findings := lint.Run(files, lint.Options{Mapper: m})
			if err := report.Write(cmd.OutOrStdout(), format, findings, report.Meta{Version: version.Version, URL: version.URL}); err != nil {
				return err
			}
			if lint.Failed(findings, threshold) {
				return errFindings
			}
			return nil
		},
	}
	mf.add(cmd, false)
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: "+strings.Join(report.Formats, ", "))
	cmd.Flags().StringVar(&failOn, "fail-on", "warning", "lowest severity that causes exit status 1: error, warning, info or none")
	return cmd
}

func validFormat(f string) bool {
	for _, x := range report.Formats {
		if x == f {
			return true
		}
	}
	return false
}
