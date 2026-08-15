package cli

import (
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func planCommands() []*cobra.Command {
	return []*cobra.Command{
		statusCommand(),
		graphCommand(),
		applyCommand(),
		applyDryCommand(),
		reconcileCommand(),
		stopPlanCommand(),
	}
}

func statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show the applied plan, its status and its components",
		GroupID: groupPlan,
		Args:    cobra.NoArgs,
		Long: `Show the applied plan, its status, the last error if any, and every
component — one request instead of two when polling.

` + apiNote(http.MethodGet, "/v1/plan/status"),
		Example: `  keystonectl status

  # Typical output
  {
    "planPath": "plan.toml",
    "status": "applied",
    "components": [ ... ]
  }`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodGet, agentAddr+"/v1/plan/status", nil)
		}),
	}
}

func graphCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "graph",
		Short:   "Show the dependency graph and a valid start order",
		GroupID: groupPlan,
		Args:    cobra.NoArgs,
		Long: `Show the plan's nodes, its edges (from a dependency to its dependents)
and a topological start order.

` + apiNote(http.MethodGet, "/v1/plan/graph"),
		Example: `  keystonectl graph

  # Typical output
  {
    "nodes": ["db", "cache", "api"],
    "edges": {"db": ["cache"], "cache": ["api"]},
    "order": ["db", "cache", "api"]
  }`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodGet, agentAddr+"/v1/plan/graph", nil)
		}),
	}
}

func applyCommand() *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:     "apply <plan.toml>",
		Short:   "Upload and apply a deployment plan",
		GroupID: groupPlan,
		Args:    cobra.ExactArgs(1),
		Long: `Upload a plan and converge the device to it.

The plan is sent as content: the API deliberately refuses to load a path from
the device's own filesystem, which would make it a file-read primitive. The path
you give here is read locally.

Re-applying an unchanged plan is a no-op. Components that are unchanged, alive
and supervised are left running rather than restarted.

` + apiNote(http.MethodPost, "/v1/plan/apply"),
		Example: `  keystonectl apply plan.toml

  # Validate and report the reconcile without installing or starting anything
  keystonectl apply plan.toml --dry

  # On a device, over SSH
  keystonectl --ssh ops@edge-001 apply plan.toml`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			return applyPlan(args[0], dry)
		}),
	}
	cmd.Flags().BoolVar(&dry, "dry", false, "Validate and report the reconcile, changing nothing")
	return cmd
}

func applyDryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apply-dry <plan.toml>",
		Short:   "Apply a plan in dry-run mode and show the reconcile",
		GroupID: groupPlan,
		Args:    cobra.ExactArgs(1),
		Long: `Exactly ` + "`apply --dry`" + `: validate the plan and report what applying it
would do, without installing or starting anything.

` + apiNote(http.MethodPost, "/v1/plan/apply?dry=true"),
		Example: `  keystonectl apply-dry plan.toml`,
		RunE: runs(func(cmd *cobra.Command, args []string) error {
			return applyPlan(args[0], true)
		}),
	}
	return cmd
}

func applyPlan(path string, dry bool) error {
	q := url.Values{}
	if dry {
		q.Set("dry", "true")
	}
	return upload(agentAddr+"/v1/plan/apply"+encode(q), path)
}

func reconcileCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "reconcile",
		Short:   "Repair the plan in effect",
		GroupID: groupPlan,
		Args:    cobra.NoArgs,
		Long: `Re-apply the plan that is already applied, so components that died and
ran out of restart attempts are started again. Components that are alive and
supervised are left running.

This is what ` + "`--reconcile-interval`" + ` does on a timer; run it by hand to
repair a device now.

It answers with skipped=true, and changes nothing, when no plan has been
applied, when an apply is already running, or when you stopped the plan — the
agent does not resurrect a plan an operator stopped.

Unlike ` + "`apply`" + `, it never rolls back. The plan in effect is its own
predecessor, so rolling back would stop healthy components and re-apply the
failure.

` + apiNote(http.MethodPost, "/v1/plan/reconcile"),
		Example: `  keystonectl reconcile`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodPost, agentAddr+"/v1/plan/reconcile", nil)
		}),
	}
}

func stopPlanCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "stop-plan",
		Short:   "Stop every component in the plan",
		GroupID: groupPlan,
		Args:    cobra.NoArgs,
		Long: `Stop the whole plan, in reverse dependency order, and record it as
stopped. The agent remembers that across reboots: it will not resume a plan you
stopped.

Not to be confused with ` + "`stop <component>`" + `, which stops one thing. The
plural mistake is an outage.

` + apiNote(http.MethodPost, "/v1/plan/stop"),
		Example: `  keystonectl stop-plan`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodPost, agentAddr+"/v1/plan/stop", nil)
		}),
	}
}
