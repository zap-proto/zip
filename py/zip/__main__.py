# SPDX-License-Identifier: BSD-3-Clause-Eco
"""Write the .zap schema a Python service declares.

    python3 -m zip example.orders:app -o orders.zap

One entry, because there is one artifact. What comes after it — the OpenAPI
document, the MCP tool list, the CLI, the SDKs — is generated from that file by
the shared projector, which every language reaches the same way:

    zipgen openapi -schema orders.zap
"""

from __future__ import annotations

import argparse
import importlib
import sys

from .app import App
from .zap import schema


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="python3 -m zip", description=__doc__)
    ap.add_argument("app", nargs="+", metavar="module:name",
                    help="the App to project; several become several interfaces in one file")
    ap.add_argument("-p", "--package", default="",
                    help="the schema's package name (default: the first app's name)")
    ap.add_argument("-o", "--out", default="-", help="where to write it (default: stdout)")
    args = ap.parse_args(argv)

    apps = [_load(ref) for ref in args.app]
    pkg = args.package or apps[0].name or "service"
    text = str(schema(pkg, *apps))
    if args.out == "-":
        sys.stdout.write(text)
    else:
        with open(args.out, "w") as f:
            f.write(text)
    return 0


def _load(ref: str) -> App:
    """The App a `module:name` reference points at."""
    mod, _, attr = ref.partition(":")
    if not attr:
        raise SystemExit(f"{ref}: name the App as module:name")
    app = getattr(importlib.import_module(mod), attr, None)
    if not isinstance(app, App):
        raise SystemExit(f"{ref}: {attr} is {type(app).__name__}, not a zip.App")
    return app


if __name__ == "__main__":
    raise SystemExit(main())
