// Command keystonectl is a thin client over the agent's HTTP API.
//
// The command tree lives in internal/cli so that cmd/cli-gen can walk it and
// generate the published CLI reference from the same definitions the binary
// runs on — the help and the documentation cannot disagree.
package main

import (
	"os"

	"github.com/carlosprados/keystone/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
