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

Pushes to `main` that touch `site/**` build and deploy. Pull requests build without
deploying, and fail on a broken internal link. The workflow can also be run
manually (**Actions → docs → Run workflow**) to publish out of band.

It deploys from `main` on purpose: the site describes released behaviour, so
publishing straight from `develop` would document flags that a downloaded binary
does not have yet.

## Updating the theme

```bash
cd site
hugo mod get -u github.com/McShelby/hugo-theme-relearn
hugo --gc --minify        # check it still builds
```
