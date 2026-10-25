"""Release assembly and validation with synthetic archives."""
import argparse
import gzip
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import release  # noqa: E402

VERSION, COMMIT = "1.2.3", "a" * 40


def fake_archive(directory, target, commit=COMMIT):
    name = release.archive_name(VERSION, target)
    path = directory / name
    members = [(release.executable(target), b"binary", 0o755), ("THIRD_PARTY_NOTICES.txt", b"notices", 0o644)]
    if name.endswith(".zip"):
        with zipfile.ZipFile(path, "w") as bundle:
            for member, content, _ in members:
                bundle.writestr(member, content)
    else:
        with path.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb") as compressed, tarfile.open(fileobj=compressed, mode="w") as bundle:
            for member, content, mode in members:
                entry = tarfile.TarInfo(member)
                entry.size, entry.mode = len(content), mode
                bundle.addfile(entry, io.BytesIO(content))
    manifest = {"version": VERSION, "commit": commit, "platform": target, "archive": name, "sha256": release.sha256(path)}
    (directory / f"{target}.json").write_text(json.dumps(manifest))


class AssembleTest(unittest.TestCase):
    """A derived product (no template.json): binaries and installers."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        marker = mock.patch.object(release, "TEMPLATE_MARKER", root / "absent.json")
        marker.start()
        self.addCleanup(marker.stop)
        self.artifacts, self.output = root / "native", root / "release"
        for target in release.PLATFORMS:
            (self.artifacts / target).mkdir(parents=True)
            fake_archive(self.artifacts / target, target)

    def tearDown(self):
        self.temp.cleanup()

    def assemble(self):
        release.assemble(argparse.Namespace(version=VERSION, commit=COMMIT, artifacts=str(self.artifacts), output=str(self.output)))

    def test_complete_release(self):
        self.assemble()
        names = {path.name for path in self.output.iterdir()}
        self.assertEqual(names, release.expected_names(VERSION))
        self.assertEqual({name for name in names if name.startswith("templatecontext_")}, {
            "templatecontext_1.2.3_linux_amd64.tar.gz", "templatecontext_1.2.3_linux_arm64.tar.gz",
            "templatecontext_1.2.3_darwin_arm64.tar.gz", "templatecontext_1.2.3_windows_amd64.zip"})
        for installer in release.INSTALLERS:
            text = (self.output / installer).read_text(encoding="utf-8")
            self.assertNotIn("@VERSION@", text)
            self.assertIn(VERSION, text)

    def test_tampered_asset_is_rejected(self):
        self.assemble()
        with (self.output / "install.ps1").open("a", encoding="utf-8") as stream:
            stream.write("# changed\n")
        with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
            release.validate_assets(self.output, VERSION)

    def test_unexpected_archive_member_is_rejected(self):
        target = "windows_amd64"
        path = self.artifacts / target / release.archive_name(VERSION, target)
        with zipfile.ZipFile(path, "a") as bundle:
            bundle.writestr("extra.dll", b"x")
        manifest = self.artifacts / target / f"{target}.json"
        data = json.loads(manifest.read_text())
        data["sha256"] = release.sha256(path)
        manifest.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "Unexpected archive layout"):
            self.assemble()

    def test_inconsistent_manifest_is_rejected(self):
        manifest = self.artifacts / "darwin_arm64" / "darwin_arm64.json"
        data = json.loads(manifest.read_text())
        data["commit"] = "b" * 40
        manifest.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "Inconsistent release manifest"):
            self.assemble()


PROMPT = """# New product @VERSION@
Commit @COMMIT@ of @DATE@.
curl -fsSLO "$base/pocketcontext-template-@VERSION@.tar.gz"
"""


class TemplateAssembleTest(unittest.TestCase):
    """The template (template.json present): the prompt, the source archive and checksums, no binaries."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.repo, self.artifacts, self.output = root / "repo", root / "native", root / "release"
        (self.repo / "prompts").mkdir(parents=True)
        (self.repo / "template.json").write_text("{}\n")
        (self.repo / "go.mod").write_text("module example\n")
        (self.repo / "prompts" / "new-product.md").write_text(PROMPT)
        (self.repo / "untracked.txt").write_text("not committed\n")
        git = ["git", "-C", str(self.repo), "-c", "user.name=t", "-c", "user.email=t@example.localhost"]
        subprocess.run(git[:3] + ["init", "-q"], check=True)
        subprocess.run(git + ["add", "template.json", "go.mod", "prompts"], check=True)
        subprocess.run(git + ["commit", "-q", "-m", "synthetic"], check=True)
        self.commit = subprocess.run(git[:3] + ["rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()
        for name, value in (("ROOT", self.repo), ("TEMPLATE_MARKER", self.repo / "template.json"),
                            ("PROMPT_SOURCE", self.repo / "prompts" / "new-product.md")):
            patch = mock.patch.object(release, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        for target in release.PLATFORMS:
            (self.artifacts / target).mkdir(parents=True)
            fake_archive(self.artifacts / target, target, self.commit)

    def tearDown(self):
        self.temp.cleanup()

    def assemble(self):
        release.assemble(argparse.Namespace(version=VERSION, commit=self.commit, artifacts=str(self.artifacts), output=str(self.output)))

    def test_prompt_release(self):
        self.assemble()
        names = {path.name for path in self.output.iterdir()}
        self.assertEqual(names, {"new-product.md", "pocketcontext-template-1.2.3.tar.gz", "checksums.txt"})
        self.assertEqual(names, release.expected_names(VERSION))
        prompt = (self.output / "new-product.md").read_text(encoding="utf-8")
        self.assertNotIn("@", prompt)
        self.assertIn(f"# New product {VERSION}", prompt)
        self.assertIn(f"Commit {self.commit} of ", prompt)
        with tarfile.open(self.output / "pocketcontext-template-1.2.3.tar.gz", "r:gz") as bundle:
            files = {entry.name for entry in bundle.getmembers() if entry.isreg()}
        self.assertEqual(files, {"pocketcontext-template-1.2.3/template.json", "pocketcontext-template-1.2.3/go.mod",
                                 "pocketcontext-template-1.2.3/prompts/new-product.md"})

    def test_missing_placeholder_is_rejected(self):
        (self.repo / "prompts" / "new-product.md").write_text(PROMPT.replace("@DATE@", "today"))
        with self.assertRaisesRegex(ValueError, "placeholder @DATE@ is missing"):
            self.assemble()

    def test_tampered_source_is_rejected(self):
        self.assemble()
        with (self.output / "pocketcontext-template-1.2.3.tar.gz").open("ab") as stream:
            stream.write(b"x")
        with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
            release.validate_assets(self.output, VERSION)


class NamesTest(unittest.TestCase):
    def test_names(self):
        self.assertEqual(release.REPOSITORY, "pocketcontext-labs/pocketcontext-template")
        self.assertEqual(release.executable("linux_amd64"), "templatecontext")
        self.assertEqual(release.executable("darwin_arm64"), "templatecontext")
        self.assertEqual(release.executable("windows_amd64"), "templatecontext.exe")
        self.assertEqual(release.archive_name("0.4.0", "linux_arm64"), "templatecontext_0.4.0_linux_arm64.tar.gz")
        self.assertEqual(release.archive_name("0.4.0", "windows_amd64"), "templatecontext_0.4.0_windows_amd64.zip")


class VersionTest(unittest.TestCase):
    def test_versions(self):
        self.assertEqual(release.version("0.1.0"), "0.1.0")
        for bad in ("v0.1.0", "0.1", "0.1.0-rc1", "01.1.0"):
            with self.assertRaises(ValueError):
                release.version(bad)


if __name__ == "__main__":
    unittest.main()
