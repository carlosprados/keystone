#!/usr/bin/env python3
"""Smoke-check the built site's structure.

These are the failures a Hugo build happily produces and no other check sees: the
page compiles, the diagrams are fine, the spec is in sync, and the layout is broken.
Each rule below exists because it went wrong or would be invisible if it did.

Usage:
    hugo --gc --minify        # in site/
    python3 site/scripts/check-layout.py [--public site/public]
"""
import argparse
import pathlib
import re
import sys

# Chapters that must appear in the sidebar of every page. A menu that silently
# loses a chapter is the kind of regression nobody notices until a reader does.
EXPECTED_CHAPTERS = [
    "Basics",
    "Core concepts",
    "Examples",
    "How it works inside",
    "Security",
    "Control planes",
    "Operations",
    "Reference",
]

# Text emitted by our own sidebar-footer partial. It must live in the sidebar
# footer and nowhere else — putting it in custom-footer.html renders it on top of
# the site title and the search box, which is exactly what happened once.
FOOTER_MARKER = "Documenting Keystone"


def strip_tags(html: str) -> str:
    return re.sub(r"<[^>]+>", " ", html)


def region(html: str, element_id: str) -> str | None:
    """Return the inner HTML of an element by id, brace-matching on div depth.

    The built HTML is minified, so this walks tags rather than trusting newlines.
    """
    # The id must end at a boundary: "R-footer" must not match "R-footer-margin".
    # A quantifier like {0,1} would be read as an f-string field, so use "?".
    m = re.search(rf'<(\w+)[^>]*\bid=["\']?{re.escape(element_id)}(?=["\'\s>])[^>]*>', html)
    if not m:
        return None
    tag = m.group(1)
    pos = m.end()
    depth = 1
    for t in re.finditer(rf"</?{tag}\b[^>]*>", html[pos:]):
        if t.group(0).startswith("</"):
            depth -= 1
            if depth == 0:
                return html[pos : pos + t.start()]
        else:
            depth += 1
    return html[pos:]


def check_page(path: pathlib.Path, html: str) -> list[str]:
    problems: list[str] = []

    # One H1. Relearn renders the front-matter title as the heading, so an H1 in
    # the page body shows the title twice.
    h1s = re.findall(r"<h1[^>]*>(.*?)</h1>", html, re.S)
    if len(h1s) == 0:
        problems.append("no <h1>: the page has no heading")
    elif len(h1s) > 1:
        titles = [strip_tags(h).strip()[:40] for h in h1s]
        problems.append(f"{len(h1s)} <h1> elements ({titles}) — remove the H1 from the "
                        f"markdown and let the front-matter title render it")

    # A non-empty title, or the browser tab and the search index read "Untitled".
    t = re.search(r"<title>(.*?)</title>", html, re.S)
    if not t or not t.group(1).strip():
        problems.append("empty or missing <title>")

    # The sidebar footer must exist and carry our version line.
    footer = region(html, "R-footer")
    if footer is None:
        problems.append("no #R-footer: the sidebar footer partial did not render")
    elif FOOTER_MARKER not in footer:
        problems.append(f"#R-footer does not contain {FOOTER_MARKER!r}")

    # ...and that line must not leak into the sidebar header, over the site title
    # and the search box. This is the bug that made the menu look broken.
    header = region(html, "R-header")
    if header is not None and FOOTER_MARKER in header:
        problems.append(f"{FOOTER_MARKER!r} is inside #R-header, on top of the title "
                        f"and the search box — it belongs in menu-footer.html, not "
                        f"custom-footer.html")

    # The whole chapter list, on every page.
    missing = [c for c in EXPECTED_CHAPTERS if f">{c}<" not in html]
    if missing:
        problems.append(f"chapters missing from the sidebar: {missing}")

    # Unrendered shortcodes: a typo in a shortcode name leaves its source in the
    # page instead of failing the build. Code blocks are excluded — a Prometheus
    # rule or a Helm template legitimately contains brace syntax, and only the
    # shortcode delimiters proper are a real signal.
    prose = re.sub(r"<(pre|code)\b.*?</\1>", " ", html, flags=re.S)
    for leak in ("{{%", "{{<"):
        if leak in prose:
            at = prose.find(leak)
            snippet = strip_tags(prose[max(0, at - 40) : at + 60]).strip()
            problems.append(f"unrendered shortcode {leak!r} near: {snippet!r}")
            break

    # Hugo's marker for a URL it refused to render, usually a bad link in a param.
    if "ZgotmplZ" in html:
        problems.append("ZgotmplZ in the output: Hugo refused to render a URL")

    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--public", default="site/public")
    args = ap.parse_args()

    root = pathlib.Path(args.public)
    if not root.is_dir():
        print(f"::error::{root} does not exist — run `hugo` first")
        return 1

    # Content pages only: 404 and the generated search page have no sidebar.
    pages = [p for p in sorted(root.rglob("index.html"))
             if "searchpage" not in p.name]
    if not pages:
        print(f"::error::no pages found under {root}")
        return 1

    failures = 0
    for page in pages:
        problems = check_page(page, page.read_text(encoding="utf-8", errors="replace"))
        rel = page.relative_to(root)
        for problem in problems:
            print(f"::error file=site/public/{rel}::{problem}")
            failures += 1

    print(f"checked {len(pages)} pages")
    if failures:
        print(f"\n{failures} layout problem(s). These are invisible to the Hugo build, "
              f"the diagram check and the OpenAPI check — that is why this exists.")
        return 1
    print("layout is sound")
    return 0


if __name__ == "__main__":
    sys.exit(main())
