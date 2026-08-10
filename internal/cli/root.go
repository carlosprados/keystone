package cli

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/carlosprados/keystone/internal/version"
	"github.com/spf13/cobra"
)

// Command groups, so `keystonectl --help` reads as a map of the API rather than
// an alphabetical pile.
const (
	groupPlan       = "plan"
	groupComponents = "components"
	groupRecipes    = "recipes"
	groupAgent      = "agent"
	groupLocal      = "local"
)

// Connection settings, resolved once before any command runs.
var (
	agentAddr string
	apiToken  string
	sshDest   string

	// client carries every request: the default one, or one tunnelled over SSH
	// when --ssh names a device whose agent only listens on its own loopback.
	client = http.DefaultClient
)

// reachedCommand records that argument parsing succeeded and a command actually
// ran, which is what separates a misuse (exit 2, worth printing the usage) from
// a failure reported by the agent (exit 1, where the usage is noise).
var reachedCommand bool

const rootLong = `keystonectl controls a Keystone agent through its HTTP API.

It is a thin client: every command is one request to one endpoint, and prints
the agent's own response. Each command's help names the endpoint it calls, so
what you can do here is exactly what the API can do.

Connecting
  --addr is the agent's base URL. It defaults to http://127.0.0.1:8080, which is
  where an agent listens unless told otherwise.

  --ssh reaches an agent on another machine. The agent binds loopback by default
  and demands a token to bind anything else, so on a real device its API is only
  reachable from the device itself. --ssh carries the request there over your own
  ssh client, and --addr is then resolved on the far side.

  Each flag falls back to an environment variable: KEYSTONE_ADDR,
  KEYSTONE_API_TOKEN, KEYSTONE_SSH. A flag always wins over its variable.

Finding your way around
  keystonectl help              every command, grouped
  keystonectl help <command>    what one command does, with examples
  keystonectl <command> --help  the same`

const rootExample = `  # A local agent
  keystonectl components

  # An agent on a device, over SSH; --addr is resolved on the device
  keystonectl --ssh ops@edge-001 --addr http://127.0.0.1:9180 status

  # The same device, without repeating yourself
  export KEYSTONE_SSH=ops@edge-001
  export KEYSTONE_ADDR=http://127.0.0.1:9180
  keystonectl components`

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "keystonectl",
		Short:   "Control a Keystone agent over its HTTP API",
		Long:    rootLong,
		Example: rootExample,
		Version: fmt.Sprintf("%s (commit %s)", version.Version, version.Commit),
		// A failing request is not a usage problem; printing the whole usage
		// after it buries the error the operator needs to read.
		SilenceUsage:      true,
		PersistentPreRunE: resolveConnection,
	}

	root.SetVersionTemplate("keystonectl version {{.Version}}\n")

	// Cobra otherwise refuses to run on Windows when it was double-clicked
	// rather than launched from a shell, printing a notice instead. That is a
	// courtesy for desktop apps and a surprise for an operator's tool.
	cobra.MousetrapHelpText = ""

	root.PersistentFlags().StringVar(&agentAddr, "addr", "http://127.0.0.1:8080",
		"Agent base URL, resolved on the SSH host when --ssh is used (or KEYSTONE_ADDR)")
	root.PersistentFlags().StringVar(&apiToken, "token", "",
		"Bearer token for the agent API (or KEYSTONE_API_TOKEN)")
	root.PersistentFlags().StringVar(&sshDest, "ssh", "",
		"Reach the agent through SSH: [user@]host[:port] (or KEYSTONE_SSH)")

	for _, g := range []*cobra.Group{
		{ID: groupPlan, Title: "Plan:"},
		{ID: groupComponents, Title: "Components:"},
		{ID: groupRecipes, Title: "Recipes:"},
		{ID: groupAgent, Title: "Agent:"},
		{ID: groupLocal, Title: "Local tools:"},
	} {
		root.AddGroup(g)
	}

	root.AddCommand(planCommands()...)
	root.AddCommand(componentCommands()...)
	root.AddCommand(recipeCommands()...)
	root.AddCommand(agentCommands()...)
	root.AddCommand(localCommands()...)

	// The generated help and completion commands land in no group otherwise,
	// which Cobra reports as an error at runtime.
	root.SetHelpCommandGroupID(groupLocal)
	root.SetCompletionCommandGroupID(groupLocal)

	return root
}

// resolveConnection applies the environment fallbacks and builds the HTTP
// client. It runs before every command, including the ones that never make a
// request — cheap, and it means a bad --ssh destination is caught early.
func resolveConnection(cmd *cobra.Command, _ []string) error {
	// --addr is the one flag with a non-empty default, so "unset" cannot be
	// spotted by comparing against "": an operator whose agent listens on
	// 127.0.0.1:9180 must be able to export KEYSTONE_ADDR once instead of
	// repeating --addr on every call.
	if env := os.Getenv("KEYSTONE_ADDR"); env != "" && !cmd.Flags().Changed("addr") {
		agentAddr = env
	}
	agentAddr = strings.TrimSuffix(agentAddr, "/")

	if apiToken == "" {
		apiToken = os.Getenv("KEYSTONE_API_TOKEN")
	}
	if sshDest == "" {
		sshDest = os.Getenv("KEYSTONE_SSH")
	}
	if sshDest != "" {
		target, err := parseSSHTarget(sshDest)
		if err != nil {
			return err
		}
		client = &http.Client{Transport: sshTransport(target)}
	}
	return nil
}

// Execute runs the CLI and returns the process exit code: 2 for a misuse, 1 for
// a failure the agent or the network reported, 0 otherwise.
func Execute() int {
	if err := NewRootCommand().Execute(); err != nil {
		if !reachedCommand {
			return 2
		}
		return 1
	}
	return 0
}

// runs marks a command as reached and returns it, so a runtime failure is not
// reported as a usage error.
func runs(fn func(cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		reachedCommand = true
		return fn(cmd, args)
	}
}

// apiNote documents, in a command's own help, the endpoint it calls. Someone
// exploring with --help should never have to guess what a command does to the
// agent.
func apiNote(method, path string) string {
	return fmt.Sprintf("Calls %s %s.", method, path)
}
