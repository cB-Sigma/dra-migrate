// Command dra-migrate lints and converts Kubernetes workloads from
// device-plugin GPU requests to Dynamic Resource Allocation (DRA).
package main

import (
	"os"

	"github.com/cb-Sigma/dra-migrate/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
