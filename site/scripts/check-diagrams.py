#!/usr/bin/env python3
"""Check that every mermaid diagram on the site renders with legible text.

A diagram wider than the content column is scaled down by the browser and its text
shrinks with it, which is how a perfectly valid diagram ends up unreadable. This
renders each one and reports the *effective* font size: the native size multiplied
by the scale factor the column would impose.

Every diagram on the site is rendered in a single mermaid-cli invocation, which
launches one browser instead of one per diagram — the difference between about a
minute and a quarter of an hour on CI. If that batch fails, the script falls back to
rendering diagrams one at a time so the broken one can be named.

Usage:
    python3 site/scripts/check-diagrams.py [--budget 720] [--min 13.5]

Requires npx; mermaid-cli is fetched on demand. Chrome needs --no-sandbox on hosts
where unprivileged user namespaces are restricted, which the generated puppeteer
config handles.
"""
import argparse
import json
import pathlib
import re
import subprocess
import sys
import tempfile


def mmdc(args: list[str], timeout: int) -> subprocess.CompletedProcess:
    return subprocess.run(["npx", "-y", "@mermaid-js/mermaid-cli", *args],
                          capture_output=True, text=True, timeout=timeout)


def visible_font_sizes(svg: str) -> list[float]:
    """Font sizes of text the reader actually sees, ignoring the hidden tooltip."""
    sizes = []
    for m in re.finditer(r'<(?:text|tspan|span|p|div)[^>]*font-size:\s*([\d.]+)px', svg):
        sizes.append(float(m.group(1)))
    for m in re.finditer(r'([^{}]+)\{([^}]*?)font-size:\s*([\d.]+)px', svg):
        selector = m.group(1)
        if 'mermaidTooltip' in selector:
            continue
        if any(k in selector for k in ('text', 'Label', 'label', 'node', 'edge',
                                       'message', 'note', 'state', 'actor')):
            sizes.append(float(m.group(3)))
    return sizes


def measure(svg_text: str, budget: float) -> tuple[float, float]:
    """Return (intrinsic width, effective font size in the content column)."""
    vb = re.search(r'<svg[^>]*viewBox="[-\d.]+ [-\d.]+ ([\d.]+) ([\d.]+)"', svg_text)
    width = float(vb.group(1)) if vb else 0.0
    fonts = visible_font_sizes(svg_text)
    native = min(fonts) if fonts else 0.0
    effective = native * min(1.0, budget / width) if width else 0.0
    return width, effective


def collect(content: pathlib.Path) -> list[tuple[pathlib.Path, int, str]]:
    found = []
    for page in sorted(content.rglob('*.md')):
        for i, block in enumerate(re.finditer(r'```mermaid\n(.*?)```', page.read_text(), re.S)):
            found.append((page, i, block.group(1)))
    return found


def theme_config(config_path: pathlib.Path) -> str:
    """The site's own mermaid config, so the check sees what readers see."""
    if not config_path.is_file():
        return '{}'
    m = re.search(r'mermaidInitialize\s*=\s*"(.*)"\s*$', config_path.read_text(), re.M)
    return json.loads('"' + m.group(1) + '"') if m else '{}'


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--budget', type=float, default=720,
                    help='content column width in px (default 720)')
    ap.add_argument('--min', dest='min_font', type=float, default=13.5,
                    help='minimum acceptable effective font size in px')
    ap.add_argument('--content', default='site/content')
    ap.add_argument('--config', default='site/hugo.toml')
    args = ap.parse_args()

    diagrams = collect(pathlib.Path(args.content))
    if not diagrams:
        print(f"::error::no mermaid diagrams found under {args.content}")
        return 1

    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = pathlib.Path(tmpdir)
        pptr = tmp / 'pptr.json'
        pptr.write_text(json.dumps({"args": ["--no-sandbox", "--disable-dev-shm-usage"]}))
        cfg = tmp / 'mermaid.json'
        cfg.write_text(theme_config(pathlib.Path(args.config)))

        # One markdown file with every diagram, in order. mermaid-cli writes
        # out-1.svg, out-2.svg, … following that order.
        batch = tmp / 'all.md'
        batch.write_text('\n\n'.join(f"```mermaid\n{src}```" for _, _, src in diagrams))
        result = mmdc(['-p', str(pptr), '-c', str(cfg), '-i', str(batch),
                       '-o', str(tmp / 'out.md')], timeout=900)

        svgs: list[pathlib.Path | None] = []
        for n in range(1, len(diagrams) + 1):
            candidate = tmp / f'out-{n}.svg'
            svgs.append(candidate if candidate.exists() else None)

        if any(s is None for s in svgs):
            # Something in the batch failed. Render individually so the error can
            # be attributed to a page instead of reported for the whole site.
            print(f"batch render incomplete ({sum(s is None for s in svgs)} missing), "
                  f"falling back to one diagram at a time", file=sys.stderr)
            if result.stderr.strip():
                print(result.stderr.strip()[-400:], file=sys.stderr)
            svgs = []
            for idx, (_, _, src) in enumerate(diagrams, start=1):
                one = tmp / f'single-{idx}.mmd'
                one.write_text(src)
                out = tmp / f'single-{idx}.svg'
                mmdc(['-p', str(pptr), '-c', str(cfg), '-i', str(one), '-o', str(out)],
                     timeout=300)
                svgs.append(out if out.exists() else None)

        failures = 0
        for (page, i, _), svg in zip(diagrams, svgs):
            if svg is None:
                print(f"::error file={page}::diagram {i}: failed to render")
                failures += 1
                continue
            width, effective = measure(svg.read_text(), args.budget)
            if effective < args.min_font:
                print(f"::error file={page}::diagram {i}: {width:.0f}px wide, text renders "
                      f"at {effective:.1f}px (minimum {args.min_font}px)")
                failures += 1

    print(f"checked {len(diagrams)} diagrams")
    if failures:
        print(f"\n{failures} diagram(s) would be hard to read. Make them narrower: "
              f"switch a wide fan-out to `flowchart LR` so branches stack, split it "
              f"in two, or shorten the labels.")
        return 1
    print("all diagrams render legibly")
    return 0


if __name__ == '__main__':
    sys.exit(main())
