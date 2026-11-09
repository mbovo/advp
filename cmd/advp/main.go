// Command advp is an iputils compatible ping with an extended report:
// loss, latency percentiles and jitter over time, plus first-hop monitoring.
package main

import (
	"os"

	"github.com/mbovo/advp/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
