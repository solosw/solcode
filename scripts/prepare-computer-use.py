#!/usr/bin/env python3
"""Fetch a pinned native runtime for go:embed. No npm/Node/CGO required."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
BUNDLE = ROOT / "internal/computeruse/bundle"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", required=True, choices=("darwin", "linux", "windows"))
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    args = parser.parse_args()
    target = f"{args.os}/{args.arch}"
    lock = json.loads((BUNDLE / "release.json").read_text())
    asset = lock["assets"][target]
    dest = BUNDLE / "assets/runtime.archive"
    if not dest.exists() or hashlib.sha256(dest.read_bytes()).hexdigest() != asset["sha256"]:
        url = f'https://github.com/{lock["repository"]}/releases/download/{lock["version"]}/{asset["name"]}'
        print(f"Downloading {asset['name']}", flush=True)
        # Write atomically, and never accept a runtime whose digest is not pinned.
        with tempfile.NamedTemporaryFile(dir=dest.parent, delete=False) as tmp:
            temporary = Path(tmp.name)
            try:
                with urllib.request.urlopen(url, timeout=120) as response:
                    digest = hashlib.sha256()
                    while chunk := response.read(1024 * 1024):
                        digest.update(chunk)
                        tmp.write(chunk)
                tmp.close()
                if digest.hexdigest() != asset["sha256"]:
                    raise RuntimeError(f"SHA256 mismatch for {asset['name']}")
                os.replace(temporary, dest)
            finally:
                temporary.unlink(missing_ok=True)
    (BUNDLE / "assets/target.json").write_text(json.dumps({"target": target}) + "\n")
    print(f"Prepared {lock['version']} ({target}), SHA256 verified")


if __name__ == "__main__":
    main()
