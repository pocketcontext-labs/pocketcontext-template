"""Release assembly and validation with synthetic archives."""
import argparse
import gzip
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import release  # noqa: E402

VERSION, COMMIT = "1.2.3", "a" * 40


def fake_archive(directory, target):
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
    manifest = {"version": VERSION, "commit": COMMIT, "platform": target, "archive": name, "sha256": release.sha256(path)}
    (directory / f"{target}.json").write_text(json.dumps(manifest))


class AssembleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
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
