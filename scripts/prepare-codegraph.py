#!/usr/bin/env python3
"""Build pinned CodeGraph-Rust with the reviewed external-index patch for go:embed.

Rust/C toolchains and Git are build-time requirements only. No runtime downloads.
Native builds are simplest; cross builds require the target Rust stdlib/linker.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parent.parent
BUNDLE = ROOT / "internal/codegraphruntime/bundle"
PATCH = ROOT / "scripts/codegraph/external-index.patch"
TARGETS = {
    "darwin/arm64": "aarch64-apple-darwin",
    "darwin/amd64": "x86_64-apple-darwin",
    "linux/arm64": "aarch64-unknown-linux-musl",
    "linux/amd64": "x86_64-unknown-linux-musl",
    "windows/arm64": "aarch64-pc-windows-msvc",
    "windows/amd64": "x86_64-pc-windows-msvc",
}


def run(args, cwd=None):
    subprocess.run(args, cwd=cwd, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--os", required=True, choices=("darwin", "linux", "windows"))
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    args = parser.parse_args()
    target = f"{args.os}/{args.arch}"
    notices = BUNDLE / "notices/THIRD_PARTY_NOTICES.txt"
    if not notices.is_file() or notices.stat().st_size == 0:
        raise RuntimeError("Checked-in CodeGraph dependency notices are missing")
    lock = json.loads((BUNDLE / "release.json").read_text())
    patch_hash = hashlib.sha256(PATCH.read_bytes()).hexdigest()
    if patch_hash != lock["patch"]["sha256"]:
        raise RuntimeError("CodeGraph source patch SHA256 mismatch")
    provenance = {"target": target, "sourceCommit": lock["commit"], "patchSHA256": patch_hash}
    dest, metadata = BUNDLE / "assets/runtime.archive", BUNDLE / "assets/target.json"
    if dest.exists() and metadata.exists():
        saved = json.loads(metadata.read_text())
        if all(saved.get(k) == v for k, v in provenance.items()) and saved.get("sha256") == hashlib.sha256(dest.read_bytes()).hexdigest():
            print(f"Prepared {lock['version']} + {lock['patch']['revision']} ({target}), verified cached build")
            return
    # A commit-pinned checkout plus --locked keeps upstream code and dependencies
    # fixed. Cargo verifies downloaded registry checksums from Cargo.lock.
    cache = Path(os.environ.get("SOLCODE_CODEGRAPH_BUILD_CACHE", str(Path.home() / ".cache/solcode-codegraph-build")))
    cache.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="source-", dir=cache) as temporary:
        source = Path(temporary)
        run(["git", "init", "-q", str(source)])
        # Keep upstream source bytes compatible with the LF-pinned patch even
        # when the host Git configuration enables Windows CRLF conversion.
        run(["git", "config", "core.autocrlf", "false"], source)
        run(["git", "config", "core.eol", "lf"], source)
        run(["git", "remote", "add", "origin", f"https://github.com/{lock['repository']}.git"], source)
        run(["git", "fetch", "--depth", "1", "origin", lock["commit"]], source)
        run(["git", "checkout", "--detach", "FETCH_HEAD"], source)
        commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=source, text=True).strip()
        if commit != lock["commit"]:
            raise RuntimeError("CodeGraph source commit mismatch")
        run(["git", "apply", "--check", str(PATCH)], source)
        run(["git", "apply", str(PATCH)], source)
        cargo_target = cache / (lock["commit"] + "-" + patch_hash[:16])
        run(["cargo", "test", "--locked", "-p", "codegraph-core", "index_paths", "--target-dir", str(cargo_target)], source)
        run(["cargo", "test", "--locked", "-p", "codegraph-store", "index_lease", "--target-dir", str(cargo_target)], source)
        run(["cargo", "test", "--locked", "-p", "codegraph-store", "rebuild::tests", "--target-dir", str(cargo_target)], source)
        triple = TARGETS[target]
        run(["cargo", "build", "--locked", "--release", "-p", "codegraph-rs", "--target", triple, "--target-dir", str(cargo_target)], source)
        name = "codegraph.exe" if args.os == "windows" else "codegraph"
        binary = cargo_target / triple / "release" / name
        with tempfile.NamedTemporaryFile(dir=BUNDLE, prefix=".build-", delete=False) as out:
            archive_path = Path(out.name)
        try:
            if args.os == "windows":
                with zipfile.ZipFile(archive_path, "w", zipfile.ZIP_DEFLATED) as archive:
                    archive.write(binary, arcname=name)
            else:
                with tarfile.open(archive_path, "w:gz") as archive:
                    archive.add(binary, arcname=name)
            provenance["sha256"] = hashlib.sha256(archive_path.read_bytes()).hexdigest()
            os.replace(archive_path, dest)
            metadata.write_text(json.dumps(provenance) + "\n")
        finally:
            archive_path.unlink(missing_ok=True)
    print(f"Prepared {lock['version']} + {lock['patch']['revision']} ({target}), source/patch verified")


if __name__ == "__main__":
    main()
