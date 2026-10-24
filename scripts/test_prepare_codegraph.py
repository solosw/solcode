#!/usr/bin/env python3
"""Offline tests for source/patch pinning and generated CodeGraph payload metadata."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

SPEC = importlib.util.spec_from_file_location("prepare_codegraph", Path(__file__).with_name("prepare-codegraph.py"))
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)


class CheckedInManifestTests(unittest.TestCase):
    def test_patch_matches_release_lock(self):
        root = Path(__file__).resolve().parent.parent
        lock = json.loads((root / "internal/codegraphruntime/bundle/release.json").read_text())
        digest = hashlib.sha256((root / "scripts/codegraph/external-index.patch").read_bytes()).hexdigest()
        self.assertEqual(digest, lock["patch"]["sha256"])
        self.assertTrue(lock["patch"]["revision"])

    def test_windows_style_checkout_preserves_patch_bytes(self):
        root = Path(__file__).resolve().parent.parent
        relative = Path("scripts/codegraph/external-index.patch")
        expected = (root / relative).read_bytes()
        self.assertNotIn(b"\r\n", expected)
        with tempfile.TemporaryDirectory() as temporary:
            repo = Path(temporary) / "repo"
            checkout = Path(temporary) / "checkout"
            repo.mkdir()
            checkout.mkdir()
            (repo / ".gitattributes").write_bytes((root / ".gitattributes").read_bytes())
            (repo / relative).parent.mkdir(parents=True)
            (repo / relative).write_bytes(expected)
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "-c", "core.autocrlf=true", "-c", "core.eol=crlf", "add", "."], check=True)
            subprocess.run(["git", "-C", str(repo), "-c", "core.autocrlf=true", "-c", "core.eol=crlf",
                            "checkout-index", "--all", "--prefix=" + checkout.as_posix() + "/"], check=True)
            self.assertEqual((checkout / relative).read_bytes(), expected)
            lock = json.loads((root / "internal/codegraphruntime/bundle/release.json").read_text())
            self.assertEqual(hashlib.sha256((checkout / relative).read_bytes()).hexdigest(), lock["patch"]["sha256"])


class PrepareCodeGraphTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        self.bundle = root / "bundle"
        (self.bundle / "assets").mkdir(parents=True)
        (self.bundle / "notices").mkdir()
        (self.bundle / "notices/THIRD_PARTY_NOTICES.txt").write_text("MIT fixture")
        self.source_patch = root / "external-index.patch"
        self.source_patch.write_bytes(b"reviewed patch fixture")
        self.lock = {"repository": "sunerpy/codegraph-rust", "version": "v0.55.2",
                     "commit": "a" * 40, "patch": {"revision": "fixture-v1", "sha256": hashlib.sha256(self.source_patch.read_bytes()).hexdigest()}}
        (self.bundle / "release.json").write_text(json.dumps(self.lock))
        self.commands = []
        for replacement in [patch.object(builder, "BUNDLE", self.bundle),
                            patch.object(builder, "PATCH", self.source_patch),
                            patch.object(builder, "run", side_effect=self.run_command),
                            patch.object(builder.subprocess, "check_output", return_value=self.lock["commit"] + "\n"),
                            patch.dict(os.environ, {"SOLCODE_CODEGRAPH_BUILD_CACHE": str(root / "cache")})]:
            replacement.start()
            self.addCleanup(replacement.stop)

    def run_command(self, args, cwd=None):
        self.commands.append(args)
        if args[:2] == ["cargo", "build"]:
            triple = args[args.index("--target") + 1]
            target = Path(args[args.index("--target-dir") + 1])
            binary = target / triple / "release" / ("codegraph.exe" if "windows" in triple else "codegraph")
            binary.parent.mkdir(parents=True, exist_ok=True)
            binary.write_bytes(b"native fixture")

    def prepare(self, target="darwin/arm64"):
        goos, arch = target.split("/")
        with patch.object(sys, "argv", ["prepare-codegraph.py", "--os", goos, "--arch", arch]), contextlib.redirect_stdout(io.StringIO()):
            builder.main()

    def test_supported_platform_archives_and_provenance(self):
        for target in builder.TARGETS:
            with self.subTest(target=target):
                self.prepare(target)
                meta = json.loads((self.bundle / "assets/target.json").read_text())
                archive = self.bundle / "assets/runtime.archive"
                self.assertEqual(meta["target"], target)
                self.assertEqual(meta["sourceCommit"], self.lock["commit"])
                self.assertEqual(meta["patchSHA256"], self.lock["patch"]["sha256"])
                self.assertEqual(meta["sha256"], hashlib.sha256(archive.read_bytes()).hexdigest())
                if target.startswith("windows/"):
                    with zipfile.ZipFile(archive) as packed:
                        self.assertEqual(packed.namelist(), ["codegraph.exe"])
                else:
                    with tarfile.open(archive) as packed:
                        self.assertEqual(packed.getnames(), ["codegraph"])
        for command in self.commands:
            if command[0] == "cargo":
                self.assertIn("--locked", command)
        self.assertTrue(any("rebuild::tests" in command for command in self.commands))
        for tests in ("process::tests", "proxy::tests"):
            self.assertTrue(any(command[:7] == ["cargo", "test", "--locked", "-p", "codegraph-daemon", "--lib", tests]
                                for command in self.commands))

    def test_upstream_checkout_pins_lf_before_checkout_and_apply(self):
        self.prepare()
        checkout = self.commands.index(["git", "checkout", "--detach", "FETCH_HEAD"])
        apply = self.commands.index(["git", "apply", "--check", str(self.source_patch)])
        for command in (["git", "config", "core.autocrlf", "false"], ["git", "config", "core.eol", "lf"]):
            self.assertLess(self.commands.index(command), checkout)
        self.assertLess(checkout, apply)

    def test_matching_payload_is_reused_without_subprocesses(self):
        self.prepare()
        self.commands.clear()
        self.prepare()
        self.assertEqual(self.commands, [])

    def test_stale_provenance_or_digest_rebuilds(self):
        for field in ("sourceCommit", "patchSHA256", "sha256"):
            with self.subTest(field=field):
                self.prepare()
                metadata = self.bundle / "assets/target.json"
                saved = json.loads(metadata.read_text())
                saved[field] = "wrong"
                metadata.write_text(json.dumps(saved))
                self.commands.clear()
                self.prepare()
                self.assertTrue(self.commands)

    def test_patch_tampering_fails_before_any_build(self):
        self.source_patch.write_bytes(b"changed patch")
        with self.assertRaisesRegex(RuntimeError, "patch SHA256 mismatch"):
            self.prepare()
        self.assertEqual(self.commands, [])

    def test_source_mismatch_never_publishes_payload(self):
        with patch.object(builder.subprocess, "check_output", return_value="b" * 40):
            with self.assertRaisesRegex(RuntimeError, "source commit mismatch"):
                self.prepare()
        self.assertFalse((self.bundle / "assets/runtime.archive").exists())
        self.assertFalse(any(command[0] == "cargo" for command in self.commands))

    def test_missing_notices_fails_before_any_build(self):
        (self.bundle / "notices/THIRD_PARTY_NOTICES.txt").unlink()
        with self.assertRaisesRegex(RuntimeError, "notices are missing"):
            self.prepare()
        self.assertEqual(self.commands, [])

    def test_failed_build_preserves_previous_payload(self):
        self.prepare()
        archive = self.bundle / "assets/runtime.archive"
        metadata = self.bundle / "assets/target.json"
        previous = (archive.read_bytes(), metadata.read_bytes())
        with patch.object(builder, "run", side_effect=subprocess.CalledProcessError(1, "cargo")):
            with self.assertRaises(subprocess.CalledProcessError):
                self.prepare("linux/amd64")
        self.assertEqual((archive.read_bytes(), metadata.read_bytes()), previous)


if __name__ == "__main__":
    unittest.main()
