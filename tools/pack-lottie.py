#!/usr/bin/env python3
"""Packs Lottie JSON animations into dotLottie (.lottie) files.

A .lottie is a zip with a manifest and the animation under animations/. The
sidebar's animations are generated as JSON by design/sidebar-icons-lottie/
lottie_gen.py and packed with this:

    python3 tools/pack-lottie.py <dir with NN-key.json> apps/web/public/lottie/sidebar

Each NN-key.json becomes key.lottie, the key being the navigation entry.
"""

import json
import pathlib
import sys
import zipfile


def pack(source: pathlib.Path, target: pathlib.Path) -> None:
    animation = json.loads(source.read_text())
    key = source.stem.split("-", 1)[-1]
    manifest = {
        "version": "1",
        "generator": "omniflow tools/pack-lottie.py",
        "animations": [{"id": key, "autoplay": False, "loop": False, "speed": 1}],
    }
    out = target / f"{key}.lottie"
    with zipfile.ZipFile(out, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        archive.writestr("manifest.json", json.dumps(manifest, separators=(",", ":")))
        archive.writestr(f"animations/{key}.json", json.dumps(animation, separators=(",", ":")))
    print(f"{out.name}: {source.stat().st_size} -> {out.stat().st_size} bytes")


def main() -> None:
    source, target = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    target.mkdir(parents=True, exist_ok=True)
    for path in sorted(source.glob("*.json")):
        pack(path, target)


if __name__ == "__main__":
    main()
