// Package cli implements the dra-migrate command line interface.
package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/cb-Sigma/dra-migrate/internal/mapping"
	"github.com/cb-Sigma/dra-migrate/internal/version"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitFindings = 1
	ExitError    = 2
)

// errFindings signals that the command worked but found problems.
var errFindings = errors.New("findings")

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRoot()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, errFindings):
		return ExitFindings
	default:
		fmt.Fprintln(stderr, "Error:", err)
		return ExitError
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "dra-migrate",
		Short: "Migrate GPU workloads from device plugins to Dynamic Resource Allocation",
		Long: `dra-migrate finds Kubernetes workloads that request GPUs the device-plugin
way (resources.limits["nvidia.com/gpu"]) and rewrites them to use Dynamic
Resource Allocation: a ResourceClaimTemplate, spec.resourceClaims and
containers[].resources.claims.

  dra-migrate lint ./manifests            # report, exit 1 on findings
  dra-migrate convert --diff ./manifests  # preview the migration
  dra-migrate convert -i ./manifests      # rewrite files in place
  helm template . | dra-migrate lint -    # lint rendered Helm output
  dra-migrate cluster                     # check a live cluster (read-only)`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}
	root.SetVersionTemplate("dra-migrate {{.Version}}\n")
	root.AddCommand(newLintCmd(), newConvertCmd(), newClusterCmd(), newExplainCmd(), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "dra-migrate", version.String())
		},
	}
}

// mapperFlags are the flags that influence how resource names are mapped.
type mapperFlags struct {
	mappingFile string
	deviceClass string
	sharing     string
}

func (f *mapperFlags) add(cmd *cobra.Command, withOutputFlags bool) {
	cmd.Flags().StringVar(&f.mappingFile, "mapping-file", "", "YAML `file` with custom resource-name to DeviceClass mappings")
	if withOutputFlags {
		cmd.Flags().StringVar(&f.deviceClass, "device-class", "", "DeviceClass for full GPUs (default \""+mapping.NvidiaGPUClass+"\")")
		cmd.Flags().StringVar(&f.sharing, "sharing", "none", "opaque sharing config for '.shared' resources: none, time-slicing or mps")
	}
}

func (f *mapperFlags) mapper() (*mapping.Mapper, error) {
	opts := mapping.Options{DeviceClass: f.deviceClass, Sharing: mapping.Sharing(f.sharing)}
	if f.mappingFile != "" {
		rules, err := mapping.LoadFile(f.mappingFile)
		if err != nil {
			return nil, err
		}
		opts.Custom = rules
	}
	return mapping.New(opts)
}
