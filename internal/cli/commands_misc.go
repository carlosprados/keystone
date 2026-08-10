package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/carlosprados/keystone/internal/version"
	"github.com/spf13/cobra"
)

func agentCommands() []*cobra.Command {
	return []*cobra.Command{
		healthCommand(),
		versionCommand(),
	}
}

func localCommands() []*cobra.Command {
	return []*cobra.Command{
		sha256Command(),
	}
}

func healthCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "health",
		Short:   "Check that the agent is alive, and for how long",
		GroupID: groupAgent,
		Args:    cobra.NoArgs,
		Long: `Ask the agent whether it is alive and how long it has been up.

This endpoint is exempt from authentication and carries no component detail, so
it is safe to use as an unauthenticated probe. It is also the quickest way to
tell a wrong --addr from a genuinely broken agent.

` + apiNote(http.MethodGet, "/healthz"),
		Example: `  keystonectl health

  # Typical output
  {
    "status": "ok",
    "uptime": "17.3s",
    "closed": false,
    "time_utc": "2026-08-10T06:25:59Z"
  }`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodGet, agentAddr+"/healthz", nil)
		}),
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Show this client's version and commit",
		GroupID: groupAgent,
		Args:    cobra.NoArgs,
		Long: `Show the version and commit of keystonectl itself. This makes no request,
so it says nothing about the agent's version.`,
		Example: `  keystonectl version`,
		RunE: runs(func(*cobra.Command, []string) error {
			fmt.Printf("keystonectl version %s (commit %s)\n", version.Version, version.Commit)
			return nil
		}),
	}
}

func sha256Command() *cobra.Command {
	return &cobra.Command{
		Use:     "sha256 <file>",
		Short:   "Compute the SHA-256 of a local file",
		GroupID: groupLocal,
		Args:    cobra.ExactArgs(1),
		Long: `Compute a file's SHA-256 digest, in the form a recipe's artifact entry
expects. Purely local: no agent is contacted.`,
		Example: `  keystonectl sha256 dist/api.tar.gz

  # What it is for
  [[artifacts]]
  uri = "https://example.com/api.tar.gz"
  sha256 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			return sha256File(args[0])
		}),
	}
}

func sha256File(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(h.Sum(nil)))
	return nil
}
