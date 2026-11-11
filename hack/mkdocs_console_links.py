"""Holds the console's links into this manual to the manual as built.

The console says what a setting does in a line and links here for the rest
(frontend/src/lib/docs.ts). A page that moves or a heading that is renamed
would leave such a link landing on the top of the wrong page, or on nothing,
with no build noticing. So every build of the manual - `make docs-build`, the
CI job and Read the Docs - reads that file and fails on a target whose page
or heading anchor this build did not produce.
"""

import pathlib
import re

from mkdocs.exceptions import PluginError

LINKS = "frontend/src/lib/docs.ts"

# A target is a quoted `path.md` or `path.md#anchor` literal.
_TARGET = re.compile(r"'([A-Za-z0-9_./-]+\.md)(?:#([^']*))?'")
_ID = re.compile(r'\sid="([^"]+)"')

_anchors: dict[str, set[str]] = {}


def on_page_content(html, page, **kwargs):
    _anchors[page.file.src_uri] = set(_ID.findall(html))
    return html


def on_post_build(config, **kwargs):
    source = pathlib.Path(config["config_file_path"]).parent / LINKS
    targets = _TARGET.findall(source.read_text(encoding="utf-8"))
    if not targets:
        raise PluginError(f"read no manual links out of {LINKS}")
    problems = []
    for page, anchor in sorted(set(targets)):
        if page not in _anchors:
            problems.append(f"{page}: no such page")
        elif anchor and anchor not in _anchors[page]:
            problems.append(f"{page}#{anchor}: no such heading on that page")
    if problems:
        raise PluginError(
            f"{LINKS} links the console to parts of the manual that do not exist:\n  "
            + "\n  ".join(problems)
        )
