package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/cb-Sigma/dra-migrate/internal/cluster"
	"github.com/cb-Sigma/dra-migrate/internal/lint"
	"github.com/cb-Sigma/dra-migrate/internal/report"
	"github.com/cb-Sigma/dra-migrate/internal/version"
)

// newClients is replaced in tests.
var newClients = func(kubeconfig, kubeContext string) (cluster.Clients, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return cluster.Clients{}, fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg.UserAgent = "dra-migrate/" + version.Version
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return cluster.Clients{}, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return cluster.Clients{}, err
	}
	return cluster.Clients{Discovery: disc, Dynamic: dyn}, nil
}

func newClusterCmd() *cobra.Command {
	var (
		mf            mapperFlags
		kubeconfig    string
		kubeContext   string
		namespace     string
		format        string
		failOn        string
		skipReadiness bool
		skipWorkloads bool
	)
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Scan a live cluster for legacy GPU requests and check DRA readiness",
		Long: `Cluster connects to the cluster of the current kubeconfig context and

  1. checks DRA readiness: server version, served resource.k8s.io versions,
     DeviceClasses, ResourceSlices published by the NVIDIA DRA driver and
     whether nodes still advertise device-plugin resources;
  2. lints Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs
     and bare Pods with the same rules as "dra-migrate lint". Objects owned
     by a controller are skipped so each workload is reported once.

The command is strictly read-only: it only performs discovery and list
requests. It needs permission to list the workload kinds, nodes,
deviceclasses and resourceslices; checks it is not allowed to perform are
reported as "unknown".

Exit status is 1 when a workload finding at or above --fail-on exists or a
readiness check fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			clients, err := newClients(kubeconfig, kubeContext)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			out := cmd.OutOrStdout()

			var readiness *cluster.Readiness
			if !skipReadiness {
				readiness, err = cluster.CheckReadiness(ctx, clients)
				if err != nil {
					return err
				}
			}
			var findings []lint.Finding
			var scan *cluster.ScanResult
			if !skipWorkloads {
				scan, err = cluster.ScanWorkloads(ctx, clients, namespace)
				if err != nil {
					return err
				}
				findings = lint.Run(scan.Files, lint.Options{Mapper: m, NoPositions: true})
			}

			switch format {
			case "json":
				if findings == nil {
					findings = []lint.Finding{}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(struct {
					Readiness *cluster.Readiness `json:"readiness,omitempty"`
					Findings  []lint.Finding     `json:"findings"`
					Summary   lint.Summary       `json:"summary"`
				}{readiness, findings, lint.Summarize(findings)}); err != nil {
					return err
				}
			case "text":
				if readiness != nil {
					printReadiness(out, readiness)
				}
				if scan != nil {
					fmt.Fprintf(out, "Workloads (%d scanned)\n", scan.Scanned)
					for _, s := range scan.Skipped {
						fmt.Fprintf(out, "  skipped %s\n", s)
					}
					if err := report.Write(out, format, findings, report.Meta{}); err != nil {
						return err
					}
				}
			default:
				if err := report.Write(out, format, findings, report.Meta{Version: version.Version, URL: version.URL}); err != nil {
					return err
				}
			}
			if lint.Failed(findings, threshold) || (readiness != nil && !readiness.Ready()) {
				return errFindings
			}
			return nil
		},
	}
	mf.add(cmd, false)
	f := cmd.Flags()
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	f.StringVar(&kubeContext, "context", "", "kubeconfig context to use")
	f.StringVarP(&namespace, "namespace", "n", "", "only scan workloads in this namespace (default: all namespaces)")
	f.StringVarP(&format, "format", "f", "text", "output format: "+strings.Join(report.Formats, ", "))
	f.StringVar(&failOn, "fail-on", "warning", "lowest severity that causes exit status 1: error, warning, info or none")
	f.BoolVar(&skipReadiness, "skip-readiness", false, "do not check DRA readiness")
	f.BoolVar(&skipWorkloads, "skip-workloads", false, "do not scan workloads")
	return cmd
}

func printReadiness(w io.Writer, r *cluster.Readiness) {
	fmt.Fprintf(w, "DRA readiness (server %s)\n", r.ServerVersion)
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  [%-7s] %s: %s\n", c.Status, c.Name, c.Detail)
	}
	if len(r.ResourceSlices) > 0 {
		drivers := make([]string, 0, len(r.ResourceSlices))
		for d := range r.ResourceSlices {
			drivers = append(drivers, fmt.Sprintf("%s=%d", d, r.ResourceSlices[d]))
		}
		sort.Strings(drivers)
		fmt.Fprintf(w, "  ResourceSlices by driver: %s\n", strings.Join(drivers, ", "))
	}
	fmt.Fprintln(w)
}
