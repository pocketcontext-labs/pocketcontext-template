#!/usr/bin/env python3
"""Check that a product derived from the template repository kept none of the template's identity.

In a derived product it scans the paths and contents (text and binary) of every
tracked or new, non-ignored file, case-insensitively, for the template's
lowercase id, its repository name, its hello-world collection and its default
port, and fails on any occurrence outside the explicit allowlist below. With
--binary PATH it also scans the bytes of a built executable.

In the template itself (template.json present and go.mod still naming the
template module) the check is inverted: it confirms that the template identity
is present where a derived product must rename it, and passes. --derived forces
the derived-product check, which in the template lists everything to rename.
Standard-library Python; runs on Linux, macOS and Windows.

    python3 tests/identity_check.py [--binary bin/PRODUCT] [--derived]
"""
import argparse
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]

# The template identity, assembled from parts so this file does not contain it.
# A derived product keeps this file unchanged.
TEMPLATE_ID = 'template' + 'context'               # case-insensitive: display name and env prefix too
TEMPLATE_REPO = 'pocketcontext' + '-template'      # repository and Go module path
TEMPLATE_COLLECTION = 'greet' + 'ing'              # hello-world collection, commands, reader, tests
TEMPLATE_PORT = '80' + '98'                        # default loopback port
TEMPLATE_MODULE = 'github.com/pocketcontext-labs/' + TEMPLATE_REPO
NEEDLES = [TEMPLATE_ID, TEMPLATE_REPO, TEMPLATE_COLLECTION]
# The port is matched as a whole number in text files only (not in binaries,
# where four digits occur by chance), outside lock files full of hashes.
PORT = re.compile(rb'(?<![0-9A-Za-z])' + TEMPLATE_PORT.encode() + rb'(?![0-9A-Za-z])')
PORT_EXEMPT = {'go.sum', 'ui/pnpm-lock.yaml'}

# The marker of the template itself; a derived product deletes it.
MARKER = 'template.json'

# Files a derived product may keep with template identity in them, as
# (path relative to the repository root, reason). Add an entry only with a
# reason a reviewer can verify. Entries that no longer match anything are
# reported, so the list stays exact.
ALLOWED = [
    ('docs/origin.md', 'records the template repository and commit the product was derived from'),
]

# Exact strings in the executable that come from third-party code, as
# (string, reason). A match is allowed when it lies inside an occurrence of one
# of these strings (compared case-sensitively).
ALLOWED_BINARY = [
    ('invalid server ' + TEMPLATE_COLLECTION, 'tailscale.com/derp client error, linked in by the tailcat tunnel'),
    ('bogus ' + TEMPLATE_COLLECTION + ' %q', 'Go standard library net/http HTTP/2 server error'),
    ('{"' + TEMPLATE_COLLECTION + '": "Hello world"}', 'PocketBase jsvm types.d.ts example, embedded by the jsvm plugin'),
]

# Files and folders the template must contain with its identity, so that the
# inverted check proves the scanner still sees what a derived product renames.
TEMPLATE_PATHS = ['cmd/' + TEMPLATE_ID, 'skills/' + TEMPLATE_ID, 'pb_migrations/1810000100_' + TEMPLATE_COLLECTION + 's.js']

SKIPPED_DIRS = {'.git', 'node_modules', 'bin', 'dist'}
TOKEN = re.compile(rb'[A-Za-z0-9_./:@#+-]*')


def repository_files():
    """Tracked and new, non-ignored files; a plain walk outside a Git checkout."""
    try:
        result = subprocess.run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT,
                                capture_output=True, check=True)
        names = sorted({name for name in result.stdout.decode('utf-8').split('\0') if name})
        return [name for name in names if (ROOT / name).is_file()]
    except (OSError, subprocess.CalledProcessError):
        names = []
        for path in sorted(ROOT.rglob('*')):
            relative = path.relative_to(ROOT)
            if path.is_file() and not SKIPPED_DIRS.intersection(relative.parts):
                names.append(relative.as_posix())
        return names


def module_path():
    try:
        for line in (ROOT / 'go.mod').read_text(encoding='utf-8').splitlines():
            if line.startswith('module '):
                return line.split()[1]
    except OSError:
        pass
    return ''


def is_template():
    return (ROOT / MARKER).is_file() and module_path() == TEMPLATE_MODULE


def snippet(line, start, length, width=60):
    begin, end = max(0, start - width), min(len(line), start + length + width)
    text = line[begin:end].decode('utf-8', 'replace').replace('\t', ' ').strip()
    return ('...' if begin else '') + text + ('...' if end < len(line) else '')


def scan_repository():
    """Every occurrence outside the allowlist, as {path: [description, ...]}."""
    allowed = {path: reason for path, reason in ALLOWED}
    used, found, files = set(), {}, repository_files()
    patterns = [re.compile(re.escape(n.encode())) for n in NEEDLES]
    for name in files:
        hits = []
        lowered_name = name.lower()
        hits += [f'path contains {n!r}' for n in NEEDLES if n in lowered_name]
        data = (ROOT / name).read_bytes()
        lowered = data.lower()
        binary = b'\0' in data[:8192]
        offset = 0
        for number, line in enumerate(lowered.split(b'\n'), 1):
            spans = [(m.start(), m.end()) for p in patterns for m in p.finditer(line)]
            if not binary and name not in PORT_EXEMPT:
                spans += [(m.start(), m.end()) for m in PORT.finditer(line)]
            original = data[offset:offset + len(line)]
            for start, end in sorted(spans):
                where = f'byte {offset + start}' if binary else f'line {number}'
                hits.append(f'{where}: {snippet(original, start, end - start)}')
            offset += len(line) + 1
        if not hits:
            continue
        if name in allowed:
            used.add(name)
            continue
        found[name] = hits
    stale = [f'{path}: ALLOWED entry matches nothing ({reason})' for path, reason in ALLOWED if path not in used]
    return files, found, stale


def scan_binary(path):
    data = path.read_bytes()
    lowered = data.lower()
    covered = {}
    for text, _ in ALLOWED_BINARY:
        inner = [m.start() for n in NEEDLES for m in re.finditer(re.escape(n), text.lower())]
        for hit in re.finditer(re.escape(text.encode()), data):
            covered.update((hit.start() + i, text) for i in inner)
    used, matches = set(), []
    for needle in NEEDLES:
        for encoded, encoding in [(needle.encode(), 'utf-8'), (needle.encode('utf-16-le'), 'utf-16-le')]:
            for match in re.finditer(re.escape(encoded), lowered):
                start = match.start()
                if start in covered:
                    used.add(covered[start])
                    continue
                if encoding == 'utf-8':
                    begin = start
                    while begin > 0 and TOKEN.fullmatch(data[begin - 1:begin]):
                        begin -= 1
                    token = data[begin:TOKEN.match(data, start).end()].decode('latin-1')
                else:
                    token = data[start:start + len(encoded)].decode(encoding)
                matches.append(f'{path.name}:byte {start} ({encoding}) {token[:120]!r}')
    notes = [f'note: ALLOWED_BINARY entry {text!r} not found in this build ({reason})'
             for text, reason in ALLOWED_BINARY if text not in used]
    return matches, notes


def report(found, limit):
    lines = []
    for name, hits in found.items():
        lines.append(f'{name}: {len(hits)} occurrence(s)')
        lines += [f'  {hit}' for hit in hits[:limit]]
        if len(hits) > limit:
            lines.append(f'  ... {len(hits) - limit} more')
    return lines


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--binary', type=Path, help='also scan this executable')
    parser.add_argument('--derived', action='store_true', help='run the derived-product check even in the template')
    parser.add_argument('--limit', type=int, default=5, help='occurrences shown per file (default 5)')
    args = parser.parse_args()
    if args.binary and not args.binary.is_file():
        parser.error(f'no such executable: {args.binary}')
    files, found, stale = scan_repository()

    if is_template() and not args.derived:
        missing = [p for p in TEMPLATE_PATHS if not (ROOT / p).exists()]
        seen = {n: sum(1 for hits in found.values() for h in hits if n in h.lower()) for n in NEEDLES}
        problems = [f'expected template path missing: {p}' for p in missing]
        problems += [f'the template identity {n!r} was not found; is this still the template?' for n, c in seen.items() if c == 0]
        if args.binary:
            matches, _ = scan_binary(args.binary)
            if not matches:
                problems.append(f'{args.binary} contains no template identity; is it a template build?')
        for line in problems:
            print(line)
        if problems:
            print(f'FAIL - template mode ({MARKER} and module {TEMPLATE_MODULE}): the template identity is incomplete')
            return 1
        print(f'ok - template mode ({MARKER} and module {TEMPLATE_MODULE}): template identity present in '
              f'{len(found)} of {len(files)} files, as expected; derived-product check skipped '
              f'(run with --derived to list what a new product renames)')
        return 0

    matches, notes = (scan_binary(args.binary) if args.binary else ([], []))
    lines = report(found, args.limit) + stale + matches + notes
    for line in lines:
        print(line)
    count = sum(len(h) for h in found.values()) + len(stale) + len(matches)
    scope = f'{len(files)} files' + (f' and {args.binary}' if args.binary else '')
    if count:
        print(f'FAIL - {count} template identity occurrence(s) in {scope} '
              f'({len(found)} files); rename them (prompts/new-product.md) or allowlist them with a reason')
        return 1
    print(f'ok - no template identity in {scope}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
