#!/usr/bin/env python3
"""Check what an anonymous client on the loopback interface can reach.

tests/testdata/public_routes.json is a frozen table of method, path and whether
the server answers an anonymous request (no token, no local key, correct Host)
with a success or redirect status. It was generated from the server before it
moved onto the shared library and must keep passing unchanged. Standard-library
Python; runs on Linux, macOS and Windows with synthetic data in a temporary folder.

    python3 tests/public_routes.py --binary bin/templatecontext [--record]

--record rewrites the table from the given binary; use it only when the server's
anonymous surface is meant to change, and review the diff.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TABLE = ROOT / 'tests' / 'testdata' / 'public_routes.json'
PASSWORD = 'synthetic test password'
COLLECTION = 'greet' + 'ings'
METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE']
PATHS = [
    '/', '/index.html', '/assets/app.js', '/assets/..%2fsecret', '/bundle/login', '/bundle/login.js',
    '/_', '/_/', '/up', '/mcp', '/.well-known/oauth-protected-resource', '/.well-known/oauth-authorization-server',
    '/oauth/authorize', '/oauth/token', '/oauth/register',
    '/api/health', '/api/settings', '/api/logs', '/api/backups', '/api/batch', '/api/crons', '/api/collections',
    '/api/realtime', '/api/files/token', '/api/context/query', '/api/context/search', '/api/context/schema',
    '/api/context/maintenance', '/api/context/traces',
    '/api/bundle/instance', '/api/bundle/token', '/api/bundle/account', '/api/bundle/password', '/api/bundle/login-code',
    '/api/bundle/reader-code', '/api/bundle/redeem', '/api/bundle/flush', '/api/bundle/stop', '/api/bundle/name',
    '/api/bundle/enable-tcp', '/api/bundle/version',
    '/api/collections/users/records', '/api/collections/users/records/aaaaaaaaaaaaaaa',
    '/api/collections/users/auth-methods', '/api/collections/users/auth-with-password', '/api/collections/users/auth-refresh',
    '/api/collections/users/request-password-reset', '/api/collections/users/request-otp',
    '/api/collections/_superusers/records', '/api/collections/_superusers/auth-with-password',
    f'/api/collections/{COLLECTION}/records', f'/api/collections/{COLLECTION}/records/aaaaaaaaaaaaaaa',
    '/api/files/users/aaaaaaaaaaaaaaa/file.txt',
]


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def request(port, method, path, headers=None):
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=30)
    body = b'{}' if method in ('POST', 'PUT', 'PATCH') else None
    all_headers = {'Host': f'127.0.0.1:{port}', 'Accept': 'application/json'}
    if body is not None:
        all_headers['Content-Type'] = 'application/json'
    all_headers.update(headers or {})
    try:
        connection.request(method, path, body=body, headers=all_headers)
        response = connection.getresponse()  # returns once the headers arrive; realtime keeps streaming
        status = response.status
        data = response.read(65536) if path != '/api/realtime' else b''
        return status, data
    finally:
        connection.close()


def primary_address():
    """This computer's outbound address, without sending anything; None if there is none."""
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        try:
            s.connect(('192.0.2.1', 9))
        except OSError:
            return None
        address = s.getsockname()[0]
    return None if address.startswith('127.') or address == '0.0.0.0' else address


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--record', action='store_true', help='rewrite the frozen table from this binary')
    args = parser.parse_args()
    binary = args.binary.resolve()
    problems = []
    with tempfile.TemporaryDirectory(prefix='route-table-', ignore_cleanup_errors=True) as temp:
        home = Path(temp)
        project = home / 'project'
        project.mkdir()
        db = project / 'routes.db'
        port = free_port()
        scrubbed = ('TEMPLATE' + 'CONTEXT_', 'POCKET' + 'CONTEXT_')
        env = {k: v for k, v in os.environ.items() if not k.upper().startswith(scrubbed)}
        env.update(HOME=str(home), USERPROFILE=str(home), XDG_CACHE_HOME=str(home / 'cache'), LOCALAPPDATA=str(home / 'AppData' / 'Local'))

        def run(*command, stdin=None):
            return subprocess.run([str(binary), *command], input=stdin, capture_output=True, text=True, encoding='utf-8', env=env, timeout=120)

        result = run('init', '--db', str(db), '--port', str(port), '--password-stdin', stdin=PASSWORD + '\n')
        if result.returncode != 0:
            print(result.stderr)
            return 1
        try:
            result = run('add', '--db', str(db), 'Synthetic route table greeting')
            if result.returncode != 0:
                print(result.stderr)
                return 1
            rows = []
            for path in PATHS:
                for method in METHODS:
                    status, _ = request(port, method, path)
                    rows.append({'method': method, 'path': path, 'allow': status < 400, 'status': status})
            if args.record:
                table = [{'method': r['method'], 'path': r['path'], 'allow': r['allow']} for r in rows]
                TABLE.parent.mkdir(exist_ok=True)
                TABLE.write_text(json.dumps(table, indent=1) + '\n', encoding='utf-8')
                print(f'recorded {len(table)} rows; {sum(1 for r in table if r["allow"])} allowed')
            else:
                frozen = json.loads(TABLE.read_text(encoding='utf-8'))
                seen = {(r['method'], r['path']): r for r in rows}
                for row in frozen:
                    got = seen.get((row['method'], row['path']))
                    if got is None:
                        problems.append(f'{row["method"]} {row["path"]}: not measured')
                    elif got['allow'] != row['allow']:
                        problems.append(f'{row["method"]} {row["path"]}: allowed={got["allow"]} (HTTP {got["status"]}), frozen as {row["allow"]}')
                if len(frozen) != len(rows):
                    problems.append(f'table has {len(frozen)} rows, {len(rows)} measured')
                if not any(r['allow'] for r in frozen) or len(frozen) < 300:
                    problems.append('table looks wrong')
            # A foreign Host is refused on every route (DNS rebinding).
            for path in ('/api/bundle/instance', '/', '/api/health'):
                status, _ = request(port, 'GET', path, {'Host': f'attacker.example:{port}'})
                if status != 403:
                    problems.append(f'{path} answers a foreign Host with HTTP {status}')
            # Anonymous requests see no record.
            status, data = request(port, 'GET', f'/api/collections/{COLLECTION}/records')
            if status not in (401, 403, 404) and (status != 200 or json.loads(data).get('items')):
                problems.append(f'an anonymous request lists records: HTTP {status}')
            # The server listens on loopback only.
            address = primary_address()
            if address is not None:
                try:
                    with socket.create_connection((address, port), timeout=2):
                        problems.append(f'the server accepts connections on {address}')
                except OSError:
                    pass
            # There is no public listener to enable.
            for option in ('--domain', '--mcp', '--hosted'):
                result = run('serve', '--db', str(db), option, 'example.invalid')
                if result.returncode != 2 or 'unknown option' not in result.stderr:
                    problems.append(f'serve accepts {option}: exit {result.returncode}: {result.stderr.strip()[-200:]}')
        finally:
            run('stop', '--db', str(db))
    for line in problems:
        print('FAIL -', line)
    if problems:
        return 1
    print('ok - anonymous route table matches')
    return 0


if __name__ == '__main__':
    sys.exit(main())
