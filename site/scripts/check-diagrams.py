#!/usr/bin/env python3
"""Check that every mermaid diagram on the site renders with legible text.

A diagram wider than the content column is scaled down by the browser and its text
shrinks with it, which is how a perfectly valid diagram ends up unreadable. This
renders each one with mermaid-cli and reports the *effective* font size — the native
size multiplied by the scale factor the column would impose.

Usage:
    python3 site/scripts/check-diagrams.py [--budget 720] [--min 13.5]

Requires npx (mermaid-cli is fetched on demand). Chrome needs --no-sandbox on hosts
with unprivileged user namespaces disabled, which the generated puppeteer config
handles.
"""
import argparse, json, pathlib, re, subprocess, sys, tempfile

def render(mmd: pathlib.Path, out: pathlib.Path, pptr: pathlib.Path, cfg: pathlib.Path) -> bool:
    r = subprocess.run(
        ["npx", "-y", "@mermaid-js/mermaid-cli", "-p", str(pptr), "-c", str(cfg),
         "-i", str(mmd), "-o", str(out)],
        capture_output=True, text=True, timeout=300)
    if not out.exists():
        print(f"  render failed: {r.stderr.strip()[-300:]}", file=sys.stderr)
        return False
    return True

def visible_font_sizes(svg: str):
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

def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--budget', type=float, default=720,
                    help='content column width in px (default 720)')
    ap.add_argument('--min', dest='min_font', type=float, default=13.5,
                    help='minimum acceptable effective font size in px')
    ap.add_argument('--content', default='site/content')
    ap.add_argument('--config', default='site/hugo.toml')
    args = ap.parse_args()

    # Reuse the site's own mermaid config, so the check sees what readers see.
    theme_css = ''
    cfg_text = pathlib.Path(args.config).read_text()
    m = re.search(r'mermaidInitialize\s*=\s*"(.*)"\s*$', cfg_text, re.M)
    if m:
        theme_css = json.loads('"' + m.group(1) + '"')

    with tempfile.TemporaryDirectory() as tmp:
        tmp = pathlib.Path(tmp)
        pptr = tmp/'pptr.json'
        pptr.write_text(json.dumps({"args": ["--no-sandbox", "--disable-dev-shm-usage"]}))
        cfg = tmp/'mermaid.json'
        cfg.write_text(theme_css or '{}')

        failures = []
        checked = 0
        for page in sorted(pathlib.Path(args.content).rglob('*.md')):
            for i, block in enumerate(re.finditer(r'```mermaid\n(.*?)```', page.read_text(), re.S)):
                checked += 1
                name = f"{page.stem}-{i}"
                mmd = tmp/f"{name}.mmd"; mmd.write_text(block.group(1))
                svg = tmp/f"{name}.svg"
                if not render(mmd, svg, pptr, cfg):
                    failures.append((page, i, 'failed to render'))
                    continue
                t = svg.read_text()
                vb = re.search(r'<svg[^>]*viewBox="[-\d.]+ [-\d.]+ ([\d.]+) ([\d.]+)"', t)
                width = float(vb.group(1)) if vb else 0
                fonts = visible_font_sizes(t)
                native = min(fonts) if fonts else 0
                effective = native * min(1.0, args.budget/width) if width else 0
                if effective < args.min_font:
                    failures.append((page, i, f"{width:.0f}px wide, text renders at "
                                              f"{effective:.1f}px (min {args.min_font}px)"))

        print(f"checked {checked} diagrams")
        for page, i, why in failures:
            print(f"::error file={page}::diagram {i}: {why}")
        if failures:
            print(f"\n{len(failures)} diagram(s) would be hard to read. Make them "
                  f"narrower: switch a wide fan-out to `flowchart LR` so branches "
                  f"stack, split it in two, or shorten the labels.")
            return 1
        print("all diagrams render legibly")
        return 0

if __name__ == '__main__':
    sys.exit(main())
