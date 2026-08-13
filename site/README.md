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

## Look and feel

The site follows [carlos.enredando.me](https://carlos.enredando.me): near-black
ground with a blueprint grid, cyan `#38bdf8` as the only accent, and Space
Grotesk / Inter / JetBrains Mono. Four files carry it.

| File | Holds |
|---|---|
| `assets/css/theme-keystone-dark.css` | The dark variant. **Variables only** — Relearn inlines this file's content into a generated stylesheet, so a relative `url()` here would resolve against the wrong path |
| `assets/css/theme-keystone-light.css` | The same grammar inverted onto paper, for visitors whose system asks for light |
| `assets/css/custom.css` | Everything a variable cannot express: the grid, the glow, the grain, the scan line, mono chapter rows, the section rule. Relearn picks this file up on its own — do not add a `custom-header.html` for it |
| `assets/css/chroma-keystone-{dark,light}.css` | Code highlighting, generated with `hugo gen chromastyles --style=github-dark` (and `github`) — the same palette the blog uses |

Rules that keep it from breaking:

- **Every colour in `custom.css` comes from a variable.** Both variants define
  the same `--KEYSTONE-*` tokens (grid, glow, scan, rule, sigil), so one
  hardcoded colour is one dark smear on the light variant.
- **Do not give `#R-sidebar` or `#R-body` a `position` of your own.** The theme
  positions the sidebar and sizes the content around it; overriding either makes
  the sidebar take its width twice and pushes the content off-screen. The
  background layers sit at `z-index: -1` instead, which needs nothing from them.
- **Mermaid is themed from `params.mermaidInitialize`** in `hugo.toml`, through
  CSS custom properties rather than literals: mermaid injects that CSS inside an
  inline `<svg>`, so the variables inherit from `:root` and each variant
  recolours its own diagrams. Each `var()` carries the literal fallback that
  mermaid-cli sees when `check-diagrams.py` renders outside the page.

Fonts are **self-hosted** under `static/fonts/` — a docs site should not call
fonts.gstatic.com on every page view. The `@font-face` block in `custom.css` is
generated; regenerate it, and the woff2 files, only when a family or weight
range changes:

```bash
python3 site/scripts/fetch-fonts.py
```

## Updating the theme

```bash
cd site
hugo mod get -u github.com/McShelby/hugo-theme-relearn
hugo --gc --minify        # check it still builds
```
