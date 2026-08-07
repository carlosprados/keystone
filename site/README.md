# Documentation site

The Keystone documentation site: Hugo + the
[Relearn](https://mcshelby.github.io/hugo-theme-relearn/) theme, published to
GitHub Pages by `.github/workflows/pages.yml`.

## Local preview

Requires **Hugo extended** (the theme uses SCSS) and Go, since the theme is a Hugo
module rather than a vendored copy or a submodule.

```bash
cd site
hugo server        # http://127.0.0.1:1313/keystone/
```

Content lives in `content/`, one directory per chapter. The sidebar order comes from
each page's `weight`, and chapter landing pages list their children with the
`children` shortcode.

## Publishing

Pushes to `main` build and deploy. Pull requests build without deploying. The workflow
can also be run manually (**Actions → docs → Run workflow**) to publish out of band.

**Tags do not publish, on purpose.** Pages keys a deployment by the commit, and a
release tag points at a commit `main` has already published, so a tag build deploys a
version Pages already has and it is discarded — silently, with a green check. The header
comment of `.github/workflows/pages.yml` has the full finding; read it before adding a
trigger back.

So the version in the footer is **committed**, in `params.version` in `hugo.toml`:

1. The release PR bumps it to the version about to be cut.
2. Merging that PR is a new commit, so the deploy that lands the bump publishes it.
3. Then tag that commit. `release.yml` fails the release if the tag name and
   `params.version` disagree, so forgetting the bump is loud rather than silent.

Do not do that by hand — `task release:prepare RELEASE=v0.3.1` bumps it, verifies the
built footer shows it and opens the PR; `task release:tag RELEASE=v0.3.1` tags `main`
after the merge. See "Releases" in the top-level README.

Checks are **local**, not CI. The workflow only runs Hugo — Relearn renders mermaid in
the browser, so nothing on the server side needs one.

```bash
task docs:check              # build, then layout + broken internal links (milliseconds)
task docs:check:diagrams     # diagram legibility (headless Chromium, ~40s)
```

| Check | What it catches |
|---|---|
| `check-layout.py` | Duplicated page titles, a sidebar footer that did not render or landed over the menu, a chapter missing from the navigation, unrendered shortcodes, `ZgotmplZ`, broken internal links (absolute **and** relative — Hugo passes relative markdown links through untouched, which is the shape a typo takes) |
| `check-diagrams.py` | A diagram wider than the content column, whose text the browser shrinks below legibility. Renders all of them in one mermaid-cli launch (~40s for the whole site) |

Run both before pushing anything under `site/`. The trade-off is explicit: nothing stops
a bad diagram or a duplicated title reaching `main` except running these.

## Updating the theme

```bash
cd site
hugo mod get -u github.com/McShelby/hugo-theme-relearn
hugo --gc --minify        # check it still builds
```
