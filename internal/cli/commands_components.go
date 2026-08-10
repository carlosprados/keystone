package cli

import (
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func componentCommands() []*cobra.Command {
	return []*cobra.Command{
		componentsCommand(),
		stopCommand(),
		restartCommand(),
		restartDryCommand(),
	}
}

func componentsCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "components",
		Short:   "List components with state, PID and last health result",
		GroupID: groupComponents,
		Args:    cobra.NoArgs,
		Long: `List every component the agent knows about.

A component reported as running has a live supervision loop and, for a process,
a live PID — the state is verified, not remembered.

` + apiNote(http.MethodGet, "/v1/components"),
		Example: `  keystonectl components

  # Typical output
  [
    {
      "name": "api",
      "state": "running",
      "pid": 4123,
      "restarts": 0,
      "last_health": "healthy"
    }
  ]`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodGet, agentAddr+"/v1/components", nil)
		}),
	}
}

func stopCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "stop <component>",
		Short:   "Stop one component",
		GroupID: groupComponents,
		Args:    cobra.ExactArgs(1),
		Long: `Stop a single component. Its dependents are left running, which may well
leave them broken — check ` + "`restart-dry`" + ` first if you are unsure who depends
on it.

To stop everything, use ` + "`stop-plan`" + `.

` + apiNote(http.MethodPost, "/v1/components/{name}:stop"),
		Example: `  keystonectl stop api`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			return request(http.MethodPost, agentAddr+componentPath(args[0], "stop"), nil)
		}),
	}
}

func restartCommand() *cobra.Command {
	var (
		dry     bool
		wait    string
		timeout string
	)
	cmd := &cobra.Command{
		Use:     "restart <component>",
		Short:   "Restart one component, cascading to its dependents",
		GroupID: groupComponents,
		Args:    cobra.ExactArgs(1),
		Long: `Restart a component and cascade to its dependents according to each
dependency type: hard and soft dependencies cascade, ordering ones do not.

--wait chooses what counts as "back": ` + "`pid`" + ` returns as soon as a new PID
exists, ` + "`health`" + ` waits until the component probes healthy. Use ` + "`health`" + ` when
the next step depends on the component actually serving.

` + apiNote(http.MethodPost, "/v1/components/{name}:restart"),
		Example: `  keystonectl restart api

  # Return only once it probes healthy, giving it two minutes
  keystonectl restart api --wait health --timeout 120s

  # See what would be touched, change nothing
  keystonectl restart api --dry`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			return restartComponent(args[0], dry, wait, timeout)
		}),
	}
	cmd.Flags().BoolVar(&dry, "dry", false, "Report what would be restarted, changing nothing")
	cmd.Flags().StringVar(&wait, "wait", "", "What to wait for: pid (agent default) or health")
	cmd.Flags().StringVar(&timeout, "timeout", "", "How long to wait, as a Go duration (agent default 60s)")
	return cmd
}

func restartDryCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "restart-dry <component>",
		Short:   "Show which components a restart would stop and start",
		GroupID: groupComponents,
		Args:    cobra.ExactArgs(1),
		Long: `Exactly ` + "`restart --dry`" + `: report the planned stop and start order
without touching anything.

` + apiNote(http.MethodPost, "/v1/components/{name}:restart?dry=true"),
		Example: `  keystonectl restart-dry db

  # Typical output
  {
    "stopOrder": ["api", "cache"],
    "startOrder": ["db", "cache", "api"]
  }`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			return restartComponent(args[0], true, "", "")
		}),
	}
}

func restartComponent(name string, dry bool, wait, timeout string) error {
	q := url.Values{}
	if dry {
		q.Set("dry", "true")
	}
	if wait != "" {
		q.Set("wait", wait)
	}
	if timeout != "" {
		q.Set("timeout", timeout)
	}
	return request(http.MethodPost, agentAddr+componentPath(name, "restart")+encode(q), nil)
}
