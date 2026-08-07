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

Pushes to `main` build and deploy, as do release tags. Pull requests build without
deploying. The workflow can also be run manually (**Actions → docs → Run workflow**)
to publish out of band.

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
