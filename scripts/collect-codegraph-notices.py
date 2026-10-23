#!/usr/bin/env python3
"""Collect native CodeGraph-Rust and embedded viewer notices (Python >=3.11).

Upgrade-time only: reads the pinned source checkout and checksum-verifies crate
and npm source archives. Normal release builds use checked-in notices.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import tomllib
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
BUNDLE = ROOT / "internal/codegraphruntime/bundle"
CACHE = Path(tempfile.gettempdir()) / "solcode-native-notices"
# These platform helper crates omit license files. Their workspace license is
# included in the corresponding parent package from the same pinned Cargo.lock.
LICENSE_FAMILIES = {
    "rsqlite-vfs": "sqlite-wasm-rs",
    "rustls-platform-verifier-android": "rustls-platform-verifier",
    "winapi-i686-pc-windows-gnu": "winapi",
    "winapi-x86_64-pc-windows-gnu": "winapi",
}
LOCKED_PACKAGES = {}
MIT_TERMS = ""


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as response:
        return response.read()


def license_texts(raw):
    texts = []
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        for member in archive.getmembers():
            leaf = Path(member.name).name.lower()
            if member.isfile() and (leaf.startswith(("license", "licence", "copying", "notice", "unlicense", "authors", "copyright")) or any(part.lower() in ("licenses", "licences") for part in Path(member.name).parts)):
                if member.size > 2 * 1024 * 1024:
                    raise RuntimeError("Unexpectedly large license file")
                texts.append(member.name + "\n" + archive.extractfile(member).read().decode(errors="replace"))
    return texts


def crate_notice(package):
    name, version, checksum = package
    saved = CACHE / f"{name}-{version}-{checksum}.txt"
    if saved.exists():
        return saved.read_text()
    url = f"https://static.crates.io/crates/{name}/{name}-{version}.crate"
    try:
        raw = fetch(url)
        if hashlib.sha256(raw).hexdigest() != checksum:
            raise RuntimeError("Crate SHA256 mismatch")
        texts = license_texts(raw)
        if not texts and name in LICENSE_FAMILIES:
            parent = LOCKED_PACKAGES[LICENSE_FAMILIES[name]]
            texts = ["Workspace license supplied by locked parent package:\n" + crate_notice(parent)]
        if not texts:
            # Some workspace crates omit a root license. Read the exact source
            # revision from the checksum-verified package, never from main.
            with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
                prefix = f"{name}-{version}/"
                cargo = tomllib.loads(archive.extractfile(prefix + "Cargo.toml").read().decode())
                vcs = json.load(archive.extractfile(prefix + ".cargo_vcs_info.json"))
            repo = re.fullmatch(r"https://github.com/([^/]+/[^/]+?)(?:\.git)?/?", cargo["package"]["repository"])
            if not repo:
                raise RuntimeError("No license text or supported source repository")
            sha = vcs["git"]["sha1"]
            base = f"https://api.github.com/repos/{repo[1]}/contents"
            listing = json.loads(fetch(f"{base}?ref={sha}"))
            for entry in listing:
                if not entry["name"].lower().startswith(("license", "licence", "copying", "notice", "unlicense")):
                    continue
                payload = json.loads(fetch(entry["url"]))
                if entry["type"] == "dir":
                    for child in payload:
                        if child["type"] == "file":
                            license_file = json.loads(fetch(child["url"]))
                            texts.append(child["path"] + "\n" + base64.b64decode(license_file["content"]).decode())
                else:
                    texts.append(entry["name"] + "\n" + base64.b64decode(payload["content"]).decode())
            if not texts:
                raise RuntimeError(f"No workspace license at {repo[1]}@{sha}: {[entry['name'] for entry in listing]}")
        result = f"\n=== {name} {version} ===\nSource: {url}\nSHA256: {checksum}\n" + "\n".join(texts)
        saved.write_text(result)
        return result
    except Exception as error:
        raise RuntimeError(f"{name}-{version}: {error}") from error


def npm_notice(item):
    name, package = item
    integrity = package["integrity"]
    saved = CACHE / (hashlib.sha256(integrity.encode()).hexdigest() + ".txt")
    if saved.exists():
        return saved.read_text()
    url = package["resolved"].replace("registry.npmmirror.com", "registry.npmjs.org")
    raw = fetch(url)
    algorithm, digest = integrity.split("-", 1)
    if base64.b64encode(hashlib.new(algorithm, raw).digest()).decode() != digest:
        raise RuntimeError("Viewer integrity mismatch: " + name)
    texts = license_texts(raw)
    if not texts:
        # These two published packages contain only an MIT declaration, not a
        # separate license file. Preserve their original metadata/attribution
        # and README, plus the standard MIT permission text (no invented dates
        # or copyright holders). Fail closed for any newly missing license.
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            manifest_raw = archive.extractfile("package/package.json").read().decode()
            manifest = json.loads(manifest_raw)
            if manifest["name"] not in ("is-reference", "locate-character") or manifest["license"] != "MIT":
                raise RuntimeError("No viewer license: " + name)
            readme = archive.extractfile("package/README.md").read().decode()
        texts = ["Upstream package.json (including author and license declaration):\n" + manifest_raw,
                 "Upstream README:\n" + readme,
                 "Standard MIT permission text:\n" + MIT_TERMS]
    result = f"\n=== Viewer {name} {package['version']} ===\nSource: {url}\nIntegrity: {integrity}\n" + "\n".join(texts)
    saved.write_text(result)
    return result


def native_packages(source):
    cargo = tomllib.loads((source / "Cargo.lock").read_text())
    workspace = tomllib.loads((source / "Cargo.toml").read_text())
    roots = set()
    # Do not traverse workspace lock entries: those include dev/benchmark deps.
    for member in workspace["workspace"]["members"]:
        if member.endswith("codegraph-bench"):
            continue
        manifest = tomllib.loads((source / member / "Cargo.toml").read_text())
        for table in [manifest] + list(manifest.get("target", {}).values()):
            for kind in ("dependencies", "build-dependencies"):
                for name, spec in table.get(kind, {}).items():
                    if isinstance(spec, dict) and spec.get("workspace"):
                        spec = workspace["workspace"]["dependencies"][name]
                    if isinstance(spec, dict) and "path" in spec:
                        continue
                    roots.add(spec.get("package", name) if isinstance(spec, dict) else name)
    by_name = {}
    for package in cargo["package"]:
        if "checksum" in package:
            by_name.setdefault(package["name"], []).append(package)
    selected = set()
    queue = list(roots)
    while queue:
        for package in by_name.get(queue.pop(), []):
            key = (package["name"], package["version"], package["checksum"])
            if key not in selected:
                selected.add(key)
                queue.extend(dep.split()[0] for dep in package.get("dependencies", []))
    return sorted(selected)


def main():
    global MIT_TERMS
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    args = parser.parse_args()
    lock = json.loads((BUNDLE / "release.json").read_text())
    commit = subprocess.check_output(["git", "-C", str(args.source), "rev-parse", "HEAD"], text=True).strip()
    if commit != lock["commit"]:
        raise RuntimeError("Source does not match release.json")
    MIT_TERMS = "Permission is hereby granted" + (args.source / "LICENSE-MIT").read_text().split("Permission is hereby granted", 1)[1]
    CACHE.mkdir(exist_ok=True)
    packages = native_packages(args.source)
    LOCKED_PACKAGES.update({package[0]: package for package in packages})
    print(f"Collecting {len(packages)} checksum-verified native dependency notices", flush=True)
    with ThreadPoolExecutor(max_workers=8) as pool:
        sections = list(pool.map(crate_notice, packages))
    npm = json.loads((args.source / "ui/package-lock.json").read_text())
    frontend = [(k, v) for k, v in npm["packages"].items() if k and not v.get("dev")]
    print(f"Collecting {len(frontend)} embedded viewer dependency notices", flush=True)
    with ThreadPoolExecutor(max_workers=8) as pool:
        sections += list(pool.map(npm_notice, frontend))
    header = f"CodeGraph-Rust {lock['version']} ({commit})\nhttps://github.com/{lock['repository']}\n"
    header += "Native runtime with bundled SQLite and tree-sitter; no Node/Python runtime.\n"
    header += "All production dependencies across supported platforms, including build-only crates, are retained as a superset.\n\n"
    header += (args.source / "LICENSE-MIT").read_text()
    header += "\n=== Embedded browser viewer ===\n" + (args.source / "ui/LICENSE").read_text()
    dest = BUNDLE / "notices/THIRD_PARTY_NOTICES.txt"
    dest.parent.mkdir(parents=True, exist_ok=True)
    temporary = dest.with_suffix(".tmp")
    temporary.write_text(header + "\n" + "\n".join(sections))
    temporary.replace(dest)
    print(f"Collected {len(packages)} native and {len(frontend)} viewer notices", flush=True)


if __name__ == "__main__":
    main()
