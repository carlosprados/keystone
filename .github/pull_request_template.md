## What this changes

<!-- One paragraph: what behaviour is different after this PR, and why. -->

## Documentation review

Doctrine in this project: **no change ships without a documentation review.**
Tick what you reviewed, and say explicitly what you decided not to touch.

- [ ] `site/content/` — the pages covering what I changed
- [ ] `task openapi` run and the regenerated spec committed (if a route or a
      response type changed — CI fails otherwise)
- [ ] `site/content/reference/` — flags, environment variables, schemas
- [ ] `docs/security.md` (if the security posture or defaults moved)
- [ ] The examples chapter still works verbatim
- [ ] README (if a flag, a default or the quick start changed)
- [ ] Nothing needed, because: <!-- reason -->

## Verification

<!--
How you know it works, beyond "tests pass": what you ran, against what, and what
you observed. If it touches supervision, state or privileges, say what you checked
on a live agent.
-->

- [ ] `task test` green (`go test -race ./...`)
- [ ] Verified on a running agent
