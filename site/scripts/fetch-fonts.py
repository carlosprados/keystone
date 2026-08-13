#!/usr/bin/env python3
"""Self-host the three web fonts the docs use, and rewrite the @font-face block.

The site must not depend on fonts.gstatic.com at render time: the docs are read
on flaky networks, the theme already self-hosts its own fonts, and a third-party
request on every page view is a privacy cost with nothing to show for it.

Downloads the latin and latin-ext variable subsets from the Google Fonts css2
API into site/static/fonts/, then replaces the block delimited by
`>>> GENERATED FONTS` / `<<< GENERATED FONTS` in site/assets/css/custom.css.

Run it only when a family or a weight range changes:

    python3 site/scripts/fetch-fonts.py
"""

from __future__ import annotations

import pathlib
import re
import subprocess
import sys

# Chrome's UA: the css2 API serves woff2 only to browsers it recognises.
UA = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

FAMILIES = {
    # directory slug: (css2 family query, CSS font-family name)
    "inter": ("Inter:wght@300..700", "Inter"),
    "jetbrains-mono": ("JetBrains+Mono:wght@400..700", "JetBrains Mono"),
    "space-grotesk": ("Space+Grotesk:wght@400..700", "Space Grotesk"),
}

SUBSETS = ("latin", "latin-ext")

BEGIN = "/* >>> GENERATED FONTS"
END = "/* <<< GENERATED FONTS */"


def curl(url: str) -> bytes:
    return subprocess.check_output(["curl", "-sSfA", UA, url])


def main() -> int:
    site = pathlib.Path(__file__).resolve().parent.parent
    faces: list[str] = []

    for slug, (query, name) in FAMILIES.items():
        css = curl(f"https://fonts.googleapis.com/css2?family={query}&display=swap").decode()
        target = site / "static" / "fonts" / slug
        target.mkdir(parents=True, exist_ok=True)

        blocks = re.findall(
            r"/\* (" + "|".join(SUBSETS) + r") \*/\s*(@font-face \{.*?\})", css, re.S
        )
        if len(blocks) != len(SUBSETS):
            print(f"error: expected {len(SUBSETS)} subsets for {name}, got {len(blocks)}",
                  file=sys.stderr)
            return 1

        for subset, block in blocks:
            src = re.search(r"url\((https://[^)]+\.woff2)\)", block).group(1)
            weight = re.search(r"font-weight: ([^;]+);", block).group(1)
            unicode_range = re.search(r"unicode-range: ([^;]+);", block).group(1)
            filename = f"{slug}-{subset}.woff2"
            (target / filename).write_bytes(curl(src))
            faces.append(
                "@font-face {\n"
                f"  font-family: '{name}';\n"
                "  font-style: normal;\n"
                f"  font-weight: {weight};\n"
                "  font-display: swap;\n"
                f"  src: url('../fonts/{slug}/{filename}') format('woff2');\n"
                f"  unicode-range: {unicode_range};\n"
                "}"
            )

    custom = site / "assets" / "css" / "custom.css"
    text = custom.read_text()
    start, stop = text.find(BEGIN), text.find(END)
    if start < 0 or stop < 0:
        print(f"error: markers not found in {custom}", file=sys.stderr)
        return 1

    header = f"{BEGIN} — do not edit by hand, run site/scripts/fetch-fonts.py */\n"
    custom.write_text(text[:start] + header + "\n".join(faces) + "\n" + text[stop:])
    print(f"wrote {len(faces)} @font-face rules to {custom.relative_to(site.parent)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
