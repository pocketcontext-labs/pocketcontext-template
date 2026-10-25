#!/usr/bin/env python3
"""Check that the retired product name appears nowhere in the repository or the executable.

Scans the paths and contents (text and binary) of every tracked or new,
non-ignored file, case-insensitively. With --binary PATH it also scans the
bytes of a built executable. Exceptions are listed explicitly below with their
reasons. Standard-library Python; runs on Linux, macOS and Windows.

    python3 tests/name_check.py [--binary bin/templatecontext]
"""
import argparse
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# Assembled from parts so this file does not contain it.
NEEDLE = 'wi' + 'ki'

# The single internal note that records where TemplateContext was forked from.
ORIGIN = 'docs/origin.md'

# Repository files whose matches are unavoidable third-party content, as
# (path relative to the repository root, reason). Add an entry only after
# verifying that every match in the file comes from a third-party package.
# Entries that no longer match anything are reported, so the list stays exact.
ALLOWED = [
]

# Exact strings in the executable that come from third-party dependencies, as
# (string, reason). A match is allowed when it lies inside an occurrence of one
# of these strings (compared case-sensitively). Go packs string constants
# without separators, so list the dependency's own string, not the run of
# characters printed around the match in failures.
ALLOWED_BINARY = [
    # PocketBase's JS hook type definitions (github.com/pocketbase/pocketbase
    # plugins/jsvm/internal/types/generated/types.d.ts), embedded by the jsvm
    # plugin that runs pb_hooks; copied from Go's database/sql documentation.
    (' * For usage examples, see the ' + NEEDLE + ' page at', 'PocketBase jsvm types.d.ts (database/sql docs)'),
    ('https://golang.org/s/sql' + NEEDLE, 'PocketBase jsvm types.d.ts (database/sql docs)'),
    # Same file, from gocloud.dev blob documentation (ETag field of blob attributes).
    ('https://en.' + NEEDLE + 'pedia.org/' + NEEDLE + '/HTTP_ETag', 'PocketBase jsvm types.d.ts (gocloud.dev blob docs)'),
    # Symbol names of tailscale.com/net/dns/publicdns variables, linked in by
    # the tailcat tunnel; present only in unstripped builds.
    ('tailscale.com/net/dns/publicdns.' + NEEDLE + 'mediaDNSv4Addr', 'tailscale.com publicdns symbol (tunnel)'),
    ('tailscale.com/net/dns/publicdns.' + NEEDLE + 'mediaDNSv6Addr', 'tailscale.com publicdns symbol (tunnel)'),
]

TOKEN = re.compile(rb'[A-Za-z0-9_./:@#+-]*')
SKIPPED_DIRS = {'.git', 'node_modules', 'bin', 'dist'}


def repository_files():
    """Tracked and new, non-ignored files; a plain walk outside a Git checkout."""
    try:
        result = subprocess.run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT,
                                capture_output=True, check=True)
        names = sorted({name for name in result.stdout.decode('utf-8').split('\0') if name})
        return [name for name in names if (ROOT / name).is_file()]  # deleted but not yet staged
    except (OSError, subprocess.CalledProcessError):
        names = []
        for path in sorted(ROOT.rglob('*')):
            relative = path.relative_to(ROOT)
            if path.is_file() and not SKIPPED_DIRS.intersection(relative.parts):
                names.append(relative.as_posix())
        return names


def snippet(line, start, width=70):
    begin, end = max(0, start - width), min(len(line), start + len(NEEDLE) + width)
    text = line[begin:end].decode('utf-8', 'replace').replace('\t', ' ').strip()
    return ('...' if begin else '') + text + ('...' if end < len(line) else '')


def scan_repository():
    needle = NEEDLE.encode()
    allowed = {path: reason for path, reason in ALLOWED}
    used, matches, files = set(), [], repository_files()
    for name in files:
        if name == ORIGIN:
            continue
        if NEEDLE in name.lower():
            matches.append(f'{name}: path contains the name')
        data = (ROOT / name).read_bytes()
        if needle not in data.lower():
            continue
        if name in allowed:
            used.add(name)
            continue
        binary = b'\0' in data[:8192]
        offset = 0
        for number, line in enumerate(data.split(b'\n'), 1):
            for match in re.finditer(re.escape(needle), line.lower()):
                where = f'byte {offset + match.start()}' if binary else f'line {number}'
                matches.append(f'{name}:{where}: {snippet(line, match.start())}')
            offset += len(line) + 1
    stale = [f'{path}: ALLOWED entry matches nothing ({reason})' for path, reason in ALLOWED if path not in used]
    return files, matches + stale


def scan_binary(path):
    data = path.read_bytes()
    lowered = data.lower()
    covered = {}  # match offset -> allowed string covering it
    for text, _ in ALLOWED_BINARY:
        inner = [m.start() for m in re.finditer(NEEDLE, text.lower())]
        for hit in re.finditer(re.escape(text.encode()), data):
            covered.update((hit.start() + i, text) for i in inner)
    used, matches = set(), []
    patterns = [(NEEDLE.encode(), 'utf-8'), (NEEDLE.encode('utf-16-le'), 'utf-16-le')]
    for needle, encoding in patterns:
        for match in re.finditer(re.escape(needle), lowered):
            start = match.start()
            if encoding == 'utf-8':
                begin = start
                while begin > 0 and TOKEN.fullmatch(data[begin - 1:begin]):
                    begin -= 1
                end = TOKEN.match(data, start).end()
                token = data[begin:end].decode('latin-1')
            else:
                token = data[start:start + len(needle)].decode(encoding)
            if start in covered:
                used.add(covered[start])
                continue
            begin, end = max(0, start - 80), min(len(data), start + len(needle) + 80)
            context = ''.join(chr(b) if 32 <= b < 127 else '.' for b in data[begin:end])
            matches.append(f'{path.name}:byte {start} ({encoding}) {token!r}: {context}')
    notes = [f'note: ALLOWED_BINARY entry {text!r} not found in this build ({reason})'
             for text, reason in ALLOWED_BINARY if text not in used]
    return matches, notes


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--binary', type=Path, help='also scan this executable')
    args = parser.parse_args()
    files, matches = scan_repository()
    notes = []
    if args.binary:
        if not args.binary.is_file():
            parser.error(f'no such executable: {args.binary}')
        found, notes = scan_binary(args.binary)
        matches += found
    for line in matches + notes:
        print(line)
    scope = f'{len(files)} files' + (f' and {args.binary}' if args.binary else '')
    if matches:
        print(f'FAIL - {len(matches)} occurrences of the retired name in {scope}')
        return 1
    print(f'ok - no occurrences of the retired name in {scope}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
