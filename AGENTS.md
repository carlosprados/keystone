# Agent Design Notes

## Remote Configuration & Security

Currently, the agent supports remote recipe management via the `POST /v1/recipes` endpoint. This allows `keystonectl` to send recipes from a local machine to the agent.

### Risks
Recipes contain lifecycle scripts (`install`, `run`, `shutdown`) that are executed by the agent. Accepting recipes over an unauthenticated API is extremely dangerous in production.

### Future Security (Roadmap)
- **Identity & mTLS**: The agent must identify the caller before accepting new recipes.
- **Recipe Signing**: Similar to artifacts, recipes should be signed by a trusted authority. The agent would verify the signature before storing or executing a recipe.
- **Audit Log**: All remote configuration changes should be logged with the identity of the requester.

## Persistence
Recipes are stored in `runtime/recipes` to ensure the agent can recover its full state after a restart without needing external connectivity.
