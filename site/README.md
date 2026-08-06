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

Three checks guard the site, and they are deliberately not all on the same path:

| Check | Runs on | Why there |
|---|---|---|
| Broken internal links | every run | Milliseconds, pure shell |
| `check-layout.py` | every run | Milliseconds, pure Python. Catches duplicated titles, a sidebar footer that did not render, a chapter missing from the navigation |
| `check-diagrams.py` | **pull requests only** | Drives a headless Chromium, so it is the slow one. A diagram cannot reach `main` without passing it, but a merge or a release tag publishes without waiting on a browser |

`check-diagrams.py` renders every diagram in a single mermaid-cli invocation — one
browser launch for the whole site, about 40 seconds, rather than one launch per
diagram, which took a quarter of an hour. If the batch fails it falls back to
rendering one at a time so the offending page can be named.

It deploys from `main` on purpose: the site describes released behaviour, so
publishing straight from `develop` would document flags that a downloaded binary
does not have yet.

## Updating the theme

```bash
cd site
hugo mod get -u github.com/McShelby/hugo-theme-relearn
hugo --gc --minify        # check it still builds
```
