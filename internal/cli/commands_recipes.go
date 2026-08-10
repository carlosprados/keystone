package cli

import (
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func recipeCommands() []*cobra.Command {
	return []*cobra.Command{
		recipesCommand(),
		uploadRecipeCommand(),
		deleteRecipeCommand(),
	}
}

func recipesCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "recipes",
		Short:   "List the recipes stored in the agent",
		GroupID: groupRecipes,
		Args:    cobra.NoArgs,
		Long: `List the recipes held in the agent's recipe store, which plans can refer
to by name and version instead of by path.

` + apiNote(http.MethodGet, "/v1/recipes"),
		Example: `  keystonectl recipes

  # Typical output
  [
    "com.acme.api-1.4.0.toml"
  ]`,
		RunE: runs(func(*cobra.Command, []string) error {
			return request(http.MethodGet, agentAddr+"/v1/recipes", nil)
		}),
	}
}

func uploadRecipeCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "upload-recipe <recipe.toml>",
		Short:   "Add a recipe to the agent's store",
		GroupID: groupRecipes,
		Args:    cobra.ExactArgs(1),
		Long: `Store a recipe so plans can refer to it as name:version.

A recipe pushed this way is trusted through API authentication and is not
signature-verified — unlike a recipe the agent loads from a file, which must
carry a detached signature. Uploading is as privileged as the token that allows
it.

Adding a recipe whose name and version already exist fails with 409 unless
--force is given.

` + apiNote(http.MethodPost, "/v1/recipes"),
		Example: `  keystonectl upload-recipe com.acme.api.recipe.toml

  # Replace an existing name:version
  keystonectl upload-recipe com.acme.api.recipe.toml --force`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			q := url.Values{}
			if force {
				q.Set("force", "true")
			}
			return upload(agentAddr+"/v1/recipes"+encode(q), args[0])
		}),
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing recipe with the same name and version")
	return cmd
}

func deleteRecipeCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete-recipe <name> <version>",
		Short:   "Remove a recipe from the agent's store",
		GroupID: groupRecipes,
		Args:    cobra.ExactArgs(2),
		Long: `Remove one recipe from the store, by the name and version in its
metadata — not by filename.

` + apiNote(http.MethodDelete, "/v1/recipes/{name}/{version}"),
		Example: `  keystonectl delete-recipe com.acme.api 1.4.0`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := agentAddr + "/v1/recipes/" + url.PathEscape(args[0]) + "/" + url.PathEscape(args[1])
			return request(http.MethodDelete, target, nil)
		}),
	}
}
