#!/usr/bin/env python3
"""Build, assemble and publish the TemplateContext release assets.

Adapted from the vendored server's release process (github.com/pocketcontext/pocketcontext),
with a native Windows build.
"""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile
import time
import zipfile

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "pocketcontext-labs/pocketcontext-template"
PLATFORMS = ("linux_amd64", "linux_arm64", "darwin_arm64", "windows_amd64")
# build-tags.txt is the one list of Go build tags: SQLite features and the
# tailscale.com features the tunnel omits.
TAGS = (ROOT / "build-tags.txt").read_text(encoding="utf-8").strip()
GO_VERSION = "go1.27.1"
GO_IMAGE = "golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414"
PACKAGE = "github.com/pocketcontext-labs/pocketcontext-template/internal/bundle"
VERSION_RE = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")
COMMIT_RE = re.compile(r"[0-9a-f]{40}\Z")
# A static MinGW build may only import Windows system libraries.
WINDOWS_DLLS = {"kernel32.dll", "advapi32.dll", "msvcrt.dll", "ws2_32.dll", "user32.dll", "shell32.dll", "ole32.dll",
                "winmm.dll", "ntdll.dll", "userenv.dll", "bcrypt.dll", "bcryptprimitives.dll", "crypt32.dll", "iphlpapi.dll",
                "mswsock.dll", "powrprof.dll", "secur32.dll", "dnsapi.dll", "netapi32.dll", "synchronization.dll", "shlwapi.dll"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def output(args, **kwargs):
    # Tool output (go list JSON, pnpm) is UTF-8; Windows would otherwise decode it as cp1252.
    return run(args, text=True, encoding="utf-8", capture_output=True, **kwargs).stdout.strip()


def version(value):
    require(VERSION_RE.fullmatch(value), "Version must be X.Y.Z without a leading v or prerelease suffix")
    return value


def commit(value):
    require(COMMIT_RE.fullmatch(value), "Commit must be a full lowercase Git SHA")
    return value


def executable(target):
    return "templatecontext.exe" if target.startswith("windows_") else "templatecontext"


def archive_name(release_version, target):
    suffix = "zip" if target.startswith("windows_") else "tar.gz"
    return f"templatecontext_{release_version}_{target}.{suffix}"


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def json_objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        item, end = decoder.raw_decode(raw)
        yield item
        raw = raw[end:]


def legal_files(directory):
    return sorted(path for path in directory.rglob("*") if path.is_file()
                  and re.fullmatch(r"(?:LICENSE|LICENCE|COPYING|COPYRIGHT|NOTICE)(?:[._-].*)?", path.name, re.I))


def third_party_notices(target):
    """License and notice files of linked Go modules, bundled reader packages and runtimes."""
    environment = dict(os.environ, GOOS=target.split("_")[0], GOARCH=target.split("_")[1], CGO_ENABLED="1")
    packages = json_objects(output(["go", "list", "-deps", "-json", "-tags", TAGS, "./cmd/templatecontext"], cwd=ROOT, env=environment))
    modules = {}
    for package in packages:
        module = package.get("Module")
        if module and not module.get("Main"):
            require("Replace" not in module, "Release dependencies must not use local module replacements")
            modules[module["Path"]] = module
    sections = ["TemplateContext third-party license and copyright notices\n",
                "\n--- PocketContext server (vendored in internal/pocketcontext) ---\n\n"
                + (ROOT / "internal/pocketcontext/LICENSE").read_text(encoding="utf-8")]
    for name, module in sorted(modules.items()):
        directory = Path(module["Dir"])
        files = legal_files(directory)
        require(bool(files), f"No license file found for linked module {name}")
        for path in files:
            sections.append(f"\n--- {name} {module['Version']} / {path.relative_to(directory).as_posix()} ---\n\n" + path.read_text(encoding="utf-8"))
    goroot = Path(output(["go", "env", "GOROOT"]))
    for path in [goroot / "LICENSE", goroot / "PATENTS", *legal_files(goroot / "src/vendor")]:
        if path.is_file():
            sections.append(f"\n--- Go {GO_VERSION.removeprefix('go')} / {path.relative_to(goroot).as_posix()} ---\n\n" + path.read_text(encoding="utf-8"))
    # The reader embeds a JavaScript bundle built from these production packages.
    pnpm = shutil.which("pnpm") or shutil.which("pnpm.cmd")
    require(pnpm is not None, "pnpm is required to collect reader package notices")
    licenses = json.loads(output([pnpm, "licenses", "list", "--prod", "--json"], cwd=ROOT / "ui"))
    for declared, items in sorted(licenses.items()):
        for item in sorted(items, key=lambda entry: entry["name"]):
            for package_path, package_version in zip(item.get("paths", []), item.get("versions", [])):
                files = [path for path in legal_files(Path(package_path)) if "node_modules" not in path.relative_to(package_path).parts]
                header = f"\n--- npm {item['name']} {package_version} ({declared}) ---\n\n"
                if files:
                    sections.extend(header + path.read_text(encoding="utf-8", errors="replace") for path in files)
                else:
                    sections.append(header + f"Declared license: {declared}. The package contains no license file.\n")
    runtime = []
    if target.startswith("linux_"):
        runtime = ["musl-COPYRIGHT.txt", "gcc-COPYING.RUNTIME.txt", "gcc-COPYING3.txt"]
    elif target.startswith("windows_"):
        runtime = ["mingw-w64-runtime-COPYING.txt", "winpthreads-COPYING.txt", "gcc-COPYING.RUNTIME.txt", "gcc-COPYING3.txt"]
    for name in runtime:
        sections.append(f"\n--- Static runtime / {name} ---\n\n" + (ROOT / "scripts/licenses" / name).read_text(encoding="utf-8"))
    return "\n".join(sections).encode("utf-8")


def metadata(args):
    event = os.environ.get("GITHUB_EVENT_NAME", "")
    ref = os.environ.get("GITHUB_REF", "")
    require(event in ("push", "workflow_dispatch"), "Only tag pushes and manual build validation are supported")
    if event == "push":
        require(ref.startswith("refs/tags/v"), "Publication requires a vX.Y.Z tag push")
        release_version = version(ref.removeprefix("refs/tags/v"))
    else:
        require(ref == "refs/heads/main", "Manual validation must run from main")
        release_version = version(args.version)
    head = commit(output(["git", "rev-parse", "HEAD"], cwd=ROOT))
    run(["git", "merge-base", "--is-ancestor", head, "origin/main"], cwd=ROOT)
    if event == "push":
        require(output(["git", "rev-parse", f"{ref}^{{commit}}"], cwd=ROOT) == head,
                "Tag does not identify the checked-out commit")
    values = {"version": release_version, "commit": head, "publish": str(event == "push").lower()}
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            for key, value in values.items():
                stream.write(f"{key}={value}\n")
    print(json.dumps(values, sort_keys=True))


def pe_imports(binary):
    """Machine type and imported DLL names of a PE32+ executable."""
    data = binary.read_bytes()
    require(data[:2] == b"MZ", "Expected a PE executable")
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    require(data[pe:pe + 4] == b"PE\0\0", "Missing PE signature")
    machine, sections, _, _, _, optional_size, _ = struct.unpack_from("<HHIIIHH", data, pe + 4)
    optional = pe + 24
    require(struct.unpack_from("<H", data, optional)[0] == 0x20B, "Expected PE32+")
    import_rva = struct.unpack_from("<I", data, optional + 112 + 8)[0]
    table = optional + optional_size
    regions = [struct.unpack_from("<IIII", data, table + 40 * index + 8) for index in range(sections)]

    def offset(rva):
        for size, address, raw_size, raw in regions:
            if address <= rva < address + max(size, raw_size):
                return raw + rva - address
        raise ValueError("Import table points outside the sections")

    names, cursor = [], offset(import_rva)
    while True:
        lookup, _, _, name, thunk = struct.unpack_from("<IIIII", data, cursor)
        if not (lookup or name or thunk):
            break
        start = offset(name)
        names.append(data[start:data.index(b"\0", start)].decode("ascii").lower())
        cursor += 20
    return machine, names


def verify_binary(binary, target):
    if target.startswith("linux_"):
        with binary.open("rb") as stream:
            header = stream.read(64)
            require(header[:6] == b"\x7fELF\x02\x01", "Expected a little-endian ELF64 binary")
            machine = struct.unpack_from("<H", header, 18)[0]
            require(machine == {"linux_amd64": 62, "linux_arm64": 183}[target], "ELF architecture mismatch")
            offset = struct.unpack_from("<Q", header, 32)[0]
            size, count = struct.unpack_from("<HH", header, 54)
            require(size >= 56 and count > 0, "Invalid ELF program headers")
            for index in range(count):
                stream.seek(offset + index * size)
                kind = struct.unpack("<I", stream.read(4))[0]
                require(kind not in (2, 3), "Linux release must be static: dynamic/interpreter program header found")
    elif target.startswith("windows_"):
        machine, dlls = pe_imports(binary)
        require(machine == 0x8664, "Expected an x86-64 Windows executable")
        unexpected = [name for name in dlls if name not in WINDOWS_DLLS and not name.startswith("api-ms-win-")]
        require(not unexpected, f"Windows release must link MinGW statically; unexpected DLLs: {unexpected}")
    else:
        require(output(["lipo", "-archs", str(binary)]) == "arm64", "Expected a native arm64 Mach-O")
        libraries = output(["otool", "-L", str(binary)]).splitlines()[1:]
        require(bool(libraries), "Missing Mach-O dependency information")
        for line in libraries:
            require(line.strip().startswith(("/usr/lib/", "/System/Library/")), "Unexpected non-system macOS dependency")
        commands = output(["otool", "-l", str(binary)])
        require(re.search(r"cmd LC_BUILD_VERSION\s+cmdsize \d+\s+platform (?:1|macos)\s+minos 15\.0(?:\s|\.)", commands),
                "macOS deployment target must be 15.0")
        run(["codesign", "--verify", "--strict", str(binary)])
        signature = run(["codesign", "-dvv", str(binary)], text=True, capture_output=True)
        require("Signature=adhoc" in signature.stderr, "Expected an ad hoc macOS code signature")


def native_platform():
    system = {"Windows": "windows", "Darwin": "darwin", "Linux": "linux"}.get(platform.system(), platform.system().lower())
    machine = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower(), platform.machine())
    return f"{system}_{machine}"


def build(args):
    release_version, revision = version(args.version), commit(args.commit)
    require(output(["git", "rev-parse", "HEAD"], cwd=ROOT) == revision, "Build commit does not match checkout")
    require(not output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=ROOT),
            "Release builds require a clean checkout so the embedded commit identifies all compiled source")
    require((ROOT / "ui/dist/index.html").is_file(), "Build the reader (ui/dist) before the binary")
    target = args.platform
    require(native_platform() == target, f"Release builds must be native ({native_platform()} != {target})")
    destination = Path(args.output).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    binary_dir = destination / target
    binary_dir.mkdir(exist_ok=True)
    binary = binary_dir / executable(target)
    flags = f"-s -w -X {PACKAGE}.Version={release_version} -X {PACKAGE}.Commit={revision}"
    if target.startswith("linux_"):
        flags += " -linkmode external -extldflags '-static'"
        # The image is an immutable multiarchitecture index. apk provides the
        # native musl compiler; CGO remains enabled for the SQLite authorizer.
        script = f'''apk add --no-cache build-base
test "$(go env GOVERSION)" = {GO_VERSION}
CGO_ENABLED=1 go build -trimpath -buildvcs=false -tags "$1" -ldflags "$2" -o /out/templatecontext ./cmd/templatecontext
chown "$3:$4" /out/templatecontext
'''
        run(["docker", "run", "--rm", "--mount", f"type=bind,src={ROOT},dst=/src,readonly",
             "--mount", f"type=bind,src={binary_dir},dst=/out", "--workdir", "/src",
             GO_IMAGE, "sh", "-ec", script, "build", TAGS,
             flags, str(os.getuid()), str(os.getgid())])
    else:
        require(output(["go", "env", "GOVERSION"]) == GO_VERSION, f"Expected {GO_VERSION}")
        environment = dict(os.environ, CGO_ENABLED="1")
        if target.startswith("windows_"):
            # Link the MinGW runtime statically so no libwinpthread/libgcc DLLs are needed.
            flags += " -linkmode external -extldflags '-static'"
            environment.update(CC=os.environ.get("CC", "gcc"))
        else:
            environment.update(MACOSX_DEPLOYMENT_TARGET="15.0",
                               CGO_CFLAGS="-mmacosx-version-min=15.0", CGO_LDFLAGS="-mmacosx-version-min=15.0")
        run(["go", "build", "-trimpath", "-buildvcs=false", "-tags", TAGS,
             "-ldflags", flags, "-o", str(binary), "./cmd/templatecontext"], cwd=ROOT, env=environment)
        if target.startswith("darwin_"):
            run(["codesign", "--force", "--sign", "-", str(binary)])
    verify_binary(binary, target)
    notices = third_party_notices(target)
    stamp = int(output(["git", "show", "-s", "--format=%ct", revision], cwd=ROOT))
    archive = destination / archive_name(release_version, target)
    members = [(executable(target), binary.read_bytes(), 0o755), ("THIRD_PARTY_NOTICES.txt", notices, 0o644)]
    if target.startswith("windows_"):
        moment = max(stamp, 315532800)  # ZIP timestamps start in 1980
        when = time.gmtime(moment)[:6]
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
            for name, content, mode in members:
                entry = zipfile.ZipInfo(name, date_time=when)
                entry.external_attr = (0o100000 | mode) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                bundle.writestr(entry, content)
    else:
        with archive.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=stamp) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as bundle:
                for name, content, mode in members:
                    entry = tarfile.TarInfo(name)
                    entry.size, entry.mode, entry.mtime = len(content), mode, stamp
                    bundle.addfile(entry, io.BytesIO(content))
    manifest = {"version": release_version, "commit": revision, "platform": target,
                "archive": archive.name, "sha256": sha256(archive)}
    (destination / f"{target}.json").write_text(json.dumps(manifest, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(manifest, sort_keys=True))


def check_archive(archive, target):
    expected = {executable(target), "THIRD_PARTY_NOTICES.txt"}
    if archive.name.endswith(".zip"):
        with zipfile.ZipFile(archive) as bundle:
            entries = bundle.infolist()
            require(len(entries) == len(expected) and {e.filename for e in entries} == expected
                    and all(not e.is_dir() and e.file_size > 0 for e in entries), f"Unexpected archive layout: {archive.name}")
            require(bundle.testzip() is None, f"Corrupt archive: {archive.name}")
        return
    modes = {"templatecontext": 0o755, "THIRD_PARTY_NOTICES.txt": 0o644}
    with tarfile.open(archive, "r:gz") as bundle:
        entries = bundle.getmembers()
        require(len(entries) == len(expected) and {entry.name for entry in entries} == expected
                and all(entry.isreg() and entry.mode == modes[entry.name] and entry.size > 0 for entry in entries),
                f"Unexpected archive layout: {archive.name}")


INSTALLERS = ("install.sh", "install.ps1")


def assemble(args):
    release_version, revision = version(args.version), commit(args.commit)
    artifacts, destination = Path(args.artifacts), Path(args.output)
    require(not destination.exists() or not list(destination.iterdir()), "Assembly output must be empty")
    destination.mkdir(parents=True, exist_ok=True)
    for target in PLATFORMS:
        manifests = list(artifacts.rglob(f"{target}.json"))
        require(len(manifests) == 1, f"Expected exactly one manifest for {target}")
        manifest = json.loads(manifests[0].read_text(encoding="utf-8"))
        name = archive_name(release_version, target)
        require(manifest.get("version") == release_version and manifest.get("commit") == revision
                and manifest.get("platform") == target and manifest.get("archive") == name,
                f"Inconsistent release manifest for {target}")
        archive = manifests[0].parent / name
        require(sha256(archive) == manifest.get("sha256"), f"Artifact checksum mismatch for {target}")
        check_archive(archive, target)
        shutil.copyfile(archive, destination / name)
    for installer in INSTALLERS:
        template = (ROOT / "scripts" / (installer + ".in")).read_text(encoding="utf-8")
        require("@VERSION@" in template, f"{installer} version placeholder is missing")
        rendered = template.replace("@VERSION@", release_version)
        (destination / installer).write_bytes(rendered.encode("utf-8"))
    (destination / "install.sh").chmod(0o755)
    files = sorted(destination.iterdir())
    (destination / "checksums.txt").write_text("".join(f"{sha256(path)}  {path.name}\n" for path in files), encoding="utf-8")
    validate_assets(destination, release_version)
    print(f"Assembled {len(files) + 1} assets for v{release_version}")


def expected_names(release_version):
    return {archive_name(release_version, target) for target in PLATFORMS} | set(INSTALLERS) | {"checksums.txt"}


def validate_assets(directory, release_version):
    names = expected_names(release_version)
    require({path.name for path in directory.iterdir()} == names, f"Release must contain exactly the {len(names)} expected assets")
    checksums = {}
    for line in (directory / "checksums.txt").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None, "Malformed checksum line")
        digest, name = match.groups()
        require(name not in checksums, "Duplicate checksum entry")
        checksums[name] = digest
    require(set(checksums) == names - {"checksums.txt"}, "Checksum manifest has missing or unexpected assets")
    for name, digest in checksums.items():
        require(sha256(directory / name) == digest, f"Checksum mismatch: {name}")
    for target in PLATFORMS:
        check_archive(directory / archive_name(release_version, target), target)
    return {name: sha256(directory / name) for name in names}


def api(path, method="GET", body=None, missing_ok=False):
    command = ["gh", "api", f"repos/{REPOSITORY}/{path}", "--method", method,
               "-H", "X-GitHub-Api-Version: 2026-03-10", "-H", "Cache-Control: no-cache"]
    if body is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=None if body is None else json.dumps(body), text=True, capture_output=True)
    if result.returncode:
        if missing_ok and "(HTTP 404)" in result.stderr:
            return None
        # Do not echo raw CLI output or environment on API failures.
        raise RuntimeError(f"GitHub {method} {path} failed (exit {result.returncode})")
    return json.loads(result.stdout) if result.stdout.strip() else None


def remote_commit(tag):
    item = api(f"git/ref/tags/{tag}")["object"]
    for _ in range(8):
        if item["type"] == "commit":
            return item["sha"]
        require(item["type"] == "tag", "Release tag does not resolve to a commit")
        item = api(f"git/tags/{item['sha']}")["object"]
    raise ValueError("Excessive annotated tag nesting")


def verify_remote(release, tag, expected):
    assets = release.get("assets", [])
    require(len(assets) == len(expected) and {a["name"] for a in assets} == set(expected),
            "Remote release assets are incomplete or unexpected")
    require(all(asset["state"] == "uploaded" for asset in assets), "Remote upload is incomplete")
    with tempfile.TemporaryDirectory(prefix="templatecontext-release-verify-") as temporary:
        run(["gh", "release", "download", tag, "--repo", REPOSITORY, "--dir", temporary], capture_output=True)
        require({p.name for p in Path(temporary).iterdir()} == set(expected), "Downloaded assets differ from expected assets")
        for name, digest in expected.items():
            require(sha256(Path(temporary) / name) == digest, f"Uploaded asset checksum mismatch: {name}")


def find_release(tag):
    # The tag endpoint only finds published releases. Authorized list requests
    # also return drafts, allowing an interrupted upload to resume by release ID.
    published = api(f"releases/tags/{tag}", missing_ok=True)
    if published is not None:
        return published
    page = 1
    while True:
        releases = api(f"releases?per_page=100&page={page}")
        matches = [item for item in releases if item["tag_name"] == tag]
        require(len(matches) <= 1, "Multiple releases found for the requested tag")
        if matches:
            return api(f"releases/{matches[0]['id']}")
        if len(releases) < 100:
            return None
        page += 1


def publish(args):
    release_version, revision = version(args.version), commit(args.commit)
    tag = f"v{release_version}"
    require(os.environ.get("GITHUB_EVENT_NAME") == "push" and os.environ.get("GITHUB_REF") == f"refs/tags/{tag}",
            "Publishing is only permitted for the matching tag-push workflow")
    require(os.environ.get("GITHUB_REPOSITORY") == REPOSITORY, "Unexpected publishing repository")
    require(remote_commit(tag) == revision, "Remote tag changed since validation")
    directory = Path(args.assets)
    expected = validate_assets(directory, release_version)
    marker = f"<!-- templatecontext-release:{revision} -->"
    release = find_release(tag)
    if release is not None:
        require(marker in (release.get("body") or ""), "Existing release was not created for this validated commit")
        if not release["draft"]:
            require(release.get("immutable") is True, "Published release is not immutable; refusing to modify it")
            verify_remote(release, tag, expected)
            print(f"Published {tag} already matches all validated assets; no changes made")
            return
    else:
        base = f"https://github.com/{REPOSITORY}/releases/download/{tag}"
        body = (f"TemplateContext {release_version}: a local application: server, reader and CLI in one binary.\n\n"
                "Linux amd64 and arm64: static musl binaries. macOS arm64: macOS 15 or later, ad hoc signed. "
                "Windows amd64: statically linked MinGW build, unsigned.\n\n"
                f"Install on Linux or macOS: `curl -fsSL {base}/install.sh | sh`\n\n"
                f"Install on Windows (PowerShell): `irm {base}/install.ps1 | iex`\n\n"
                "Both installers accept a target folder (for example inside iCloud Drive, OneDrive, Dropbox or Google Drive): "
                "`TEMPLATECONTEXT_INSTALL_DIR=...` or `$env:TEMPLATECONTEXT_INSTALL_DIR=...`.\n\n"
                f"See the [README](https://github.com/{REPOSITORY}/blob/{tag}/README.md).\n\n{marker}")
        notes = ROOT / "docs" / "releases" / f"{release_version}.md"
        if notes.is_file():
            body = notes.read_text(encoding="utf-8").strip() + "\n\n---\n\n" + body
        release = api("releases", "POST", {"tag_name": tag, "target_commitish": revision,
                      "name": f"TemplateContext {release_version}", "body": body, "draft": True, "prerelease": False})
    require({asset["name"] for asset in release.get("assets", [])} <= set(expected), "Draft contains unexpected assets")
    # Retry only repairs this unpublished, commit-marked draft. Published assets
    # never pass this branch and are never overwritten or deleted.
    for asset in release.get("assets", []):
        require(api(f"releases/{release['id']}")["draft"], "Release was published while preparing uploads")
        api(f"releases/assets/{asset['id']}", "DELETE")
    run(["gh", "release", "upload", tag, "--repo", REPOSITORY,
         *[str(directory / name) for name in sorted(expected)]], capture_output=True)
    release = api(f"releases/{release['id']}")
    require(release["draft"], "Release was published before upload verification")
    verify_remote(release, tag, expected)
    require(remote_commit(tag) == revision, "Remote tag changed before publication")
    latest = api("releases/latest", missing_ok=True)
    make_latest = True
    if latest:
        latest_version = latest["tag_name"].removeprefix("v")
        require(VERSION_RE.fullmatch(latest_version), "Latest release has an unrecognized version; refusing automatic promotion")
        make_latest = tuple(map(int, release_version.split("."))) > tuple(map(int, latest_version.split(".")))
    published = api(f"releases/{release['id']}", "PATCH", {"draft": False, "prerelease": False,
                    "make_latest": str(make_latest).lower()})
    require(published.get("immutable") is True, "Release was published without immutability; check repository settings immediately")
    print(f"Published immutable {tag}; latest={str(make_latest).lower()}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("metadata")
    prepare.add_argument("--version", default="")
    prepare.set_defaults(function=metadata)
    for name, handler in (("build", build), ("assemble", assemble), ("publish", publish)):
        command = commands.add_parser(name)
        command.add_argument("--version", required=True)
        command.add_argument("--commit", required=True)
        if name == "build":
            command.add_argument("--platform", choices=PLATFORMS, required=True)
            command.add_argument("--output", default="dist")
        elif name == "assemble":
            command.add_argument("--artifacts", required=True)
            command.add_argument("--output", required=True)
        else:
            command.add_argument("--assets", required=True)
        command.set_defaults(function=handler)
    args = parser.parse_args()
    try:
        args.function(args)
    except (ValueError, RuntimeError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Release failed: {error}\n")


if __name__ == "__main__":
    main()
