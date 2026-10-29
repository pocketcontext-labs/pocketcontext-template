#!/usr/bin/env python3
"""End-to-end test of the templatecontext binary with synthetic greetings.

Runs on Linux, macOS and Windows with the standard library only. Every run uses
a temporary project folder, home and cache, and a free loopback port.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import socket
import shutil
import signal
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

PASSWORD = 'synthetic test password'
# The sign-in identity of the project's one account (bundle.AccountEmail).
ACCOUNT_EMAIL = 'account@local.invalid'
# The retired product name, assembled from parts so this file never contains it.
RETIRED = 'wi' + 'ki'
# Inherited variables of this program, of the application it was derived from and
# of that one's predecessor must not leak into the test.
SCRUBBED_PREFIXES = ('TEMPLATECONTEXT_', 'POCKET' + 'CONTEXT_', RETIRED.upper() + 'CONTEXT_')


def isolated_env(home):
    """The inherited environment with a private home and cache and no product variables."""
    env = {key: value for key, value in os.environ.items() if not key.upper().startswith(SCRUBBED_PREFIXES)}
    env.update(HOME=str(home), USERPROFILE=str(home), XDG_CACHE_HOME=str(home / 'cache'),
               LOCALAPPDATA=str(home / 'AppData' / 'Local'))
    return env


def user_cache(home):
    """os.UserCacheDir() for the isolated environment."""
    if sys.platform == 'win32':
        return home / 'AppData' / 'Local'
    if sys.platform == 'darwin':
        return home / 'Library' / 'Caches'
    return home / 'cache'


def retired_names(root):
    """Every file or directory below root (project, homes, caches, skills) named after the retired product."""
    return [str(path) for path in root.rglob('*') if RETIRED in path.name.lower()]


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


class Project:
    def __init__(self, binary, root):
        self.binary, self.root = binary, root
        self.project = root / 'Cloud Drive' / 'TemplateContext'
        self.project.mkdir(parents=True)
        self.db = self.project / 'templatecontext.db'
        self.home = root / 'home'
        self.home.mkdir()
        self.env = isolated_env(self.home)
        self.port = free_port()
        self.url = f'http://127.0.0.1:{self.port}'

    def run(self, *args, stdin=None, expect=0):
        result = subprocess.run([str(self.binary), '--db', str(self.db), *args], input=stdin, capture_output=True,
                                text=True, encoding='utf-8', env=self.env, timeout=120)
        if result.returncode != expect:
            raise AssertionError(f'{args}: exit {result.returncode}, expected {expect}\n{result.stdout}\n{result.stderr}')
        return result

    def json(self, *args, **kwargs):
        return json.loads(self.run(*args, **kwargs).stdout)

    def http(self, method, path, body=None, headers=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method,
                                         headers={'Content-Type': 'application/json', **(headers or {})})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()


class Remote:
    """Another computer: its own home and cache, an empty working folder and no project."""

    def __init__(self, binary, root, url):
        self.binary, self.root, self.url = binary, root, url
        self.home = root / 'home'
        self.work = root / 'work'
        self.home.mkdir(parents=True)
        self.work.mkdir()
        self.env = isolated_env(self.home)

    def run(self, *args, stdin=None, expect=0, url=None, env=None):
        flags = [] if url is False else ['--url', url or self.url]
        result = subprocess.run([str(self.binary), *args, *flags], input='' if stdin is None else stdin,
                                capture_output=True, text=True, encoding='utf-8', env={**self.env, **(env or {})},
                                cwd=self.work, timeout=120)
        if expect is not None and result.returncode != expect:
            raise AssertionError(f'remote {args}: exit {result.returncode}, expected {expect}\n{result.stdout}\n{result.stderr}')
        return result

    def json(self, *args, **kwargs):
        return json.loads(self.run(*args, **kwargs).stdout)

    def session(self):
        sessions = [p for p in self.root.rglob('session.json') if p.parent.name.startswith('remote-')
                    and p.parent.parent == user_cache(self.home) / 'templatecontext']
        if len(sessions) != 1:
            raise AssertionError(f'expected one remote session, found {sessions}')
        return sessions[0]


class Forward:
    """A loopback TCP forward from a different local port, like a tunnel on the wrong port."""

    def __init__(self, target):
        self.target = target
        self.listener = socket.socket()
        self.listener.bind(('127.0.0.1', 0))
        self.listener.listen(16)
        self.listener.settimeout(0.2)
        self.port = self.listener.getsockname()[1]
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self):
        with self.listener:
            while not self.stopped.is_set():
                try:
                    client, _ = self.listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    return
                self.connect(client)

    def connect(self, client):
        client.settimeout(None)
        try:
            upstream = socket.create_connection(('127.0.0.1', self.target), timeout=10)
            upstream.settimeout(None)
        except OSError:
            client.close()
            return
        for src, dst in ((client, upstream), (upstream, client)):
            threading.Thread(target=self.pipe, args=(src, dst), daemon=True).start()

    @staticmethod
    def pipe(src, dst):
        try:
            while True:
                data = src.recv(65536)
                if not data:
                    break
                dst.sendall(data)
        except OSError:
            pass
        finally:
            for sock in (src, dst):
                try:
                    sock.close()
                except OSError:
                    pass

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.stopped.set()
        self.thread.join(timeout=5)


def http_status(url):
    try:
        with urllib.request.urlopen(url, timeout=10) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


def listening(port):
    try:
        with socket.create_connection(('127.0.0.1', port), timeout=2):
            return True
    except OSError:
        return False


def check(condition, message):
    if not condition:
        raise AssertionError(message)
    print('ok -', message)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    binary = parser.parse_args().binary.resolve()
    with tempfile.TemporaryDirectory(prefix='templatecontext-e2e-') as temp:
        pc = Project(binary, Path(temp))
        try:
            scenario(pc)
            single_user_scenario(pc)
            sync_scenario(pc)
            earlier_release_scenario(pc)
            remote = Remote(binary, Path(temp) / 'other-computer', pc.url)
            remote_scenario(pc, remote)
            tunnel_scenario(pc, remote)
            codes_scenario(pc, remote)
            pc.json('stop')
            leftovers = retired_names(pc.root)
            check(not leftovers, f'no file or directory name contains the retired product name: {leftovers}')
        finally:
            subprocess.run([str(binary), '--db', str(pc.db), 'stop'], env=pc.env, capture_output=True, timeout=60)
    print('all end-to-end checks passed')


def messages(rows):
    return [row['message'] for row in rows]


def token(pc, password):
    status, body = pc.http('POST', '/api/collections/users/auth-with-password', {'identity': ACCOUNT_EMAIL, 'password': password})
    if status != 200:
        raise AssertionError(f'password sign-in: HTTP {status} {body!r}')
    return json.loads(body)['token']


def local_key(pc):
    """The private local key the server wrote to this computer's cache."""
    keys = list((user_cache(pc.home) / 'templatecontext').glob('*/local.key'))
    if len(keys) != 1:
        raise AssertionError(f'expected one local key, found {keys}')
    return keys[0].read_text(encoding='utf-8').strip()


def scenario(pc):
    usage = pc.run('help')
    text = usage.stdout + usage.stderr
    check('templatecontext' in text and RETIRED not in text.lower(), 'help names templatecontext and not the retired product')
    add_help = pc.run('add', '--help').stdout
    check(add_help.startswith('usage: templatecontext add') and '"message"' in add_help, 'add --help documents the output shape')
    check(pc.run('help', 'search').stdout == pc.run('search', '-h').stdout and 'generation' in pc.run('help', 'search').stdout,
          'help COMMAND and COMMAND -h print the same help')
    check('unknown command' in pc.run('help', 'no-such-command', expect=2).stderr, 'help refuses an unknown command')
    check('unknown command' in pc.run('ingest', 'x', expect=2).stderr, 'knowledge-base commands are not part of the template')

    pc.run('init', '--email', 'tester@example.localhost', '--port', str(pc.port), '--password-stdin', stdin=PASSWORD + '\n', expect=2)
    check(not pc.db.exists(), 'init has no --email: the project has one account without a chosen identity')
    init = pc.json('init', '--port', str(pc.port), '--password-stdin', stdin=PASSWORD + '\n')
    check(init['accountCreated'] and init['url'] == pc.url and init['newProject'] and 'email' not in init,
          'init creates the account and starts the server')
    check(pc.db.exists(), 'init writes the database to the project folder')
    config = json.loads((pc.project / 'templatecontext.json').read_text(encoding='utf-8'))
    check(sorted(config) == ['instance', 'port'], f'templatecontext.json holds only the port and project ID: {config}')
    me = pc.json('whoami')
    check(sorted(me) == ['db', 'id', 'url'] and me['db'] == str(pc.db), 'whoami reports the account ID, URL and database')
    again = pc.json('init', '--port', str(pc.port))
    check(not again['accountCreated'] and not again['newProject'] and pc.json('whoami')['id'] == me['id'],
          'init on an existing project keeps its account and password')
    check(pc.json('list') == [], 'a new project has no greetings')
    schema = pc.json('schema')
    tables = {t['name']: sorted(c['name'] for c in t['columns']) for t in schema['tables']}
    check(tables == {'greetings': ['created', 'id', 'message', 'updated']}, f'the SQL schema exposes only greetings: {tables}')
    check(schema['permissionModel'] == 'shared' and schema['search']['indexes'] == ['greetings'] and 'isolation' not in schema['search']
          and 'snapshotLimits' not in schema, 'reads use the live database and a shared greetings index')

    first = pc.json('add', 'Hello from the espresso machine ✓')
    second = pc.json('add', '-', stdin='Café greetings to everyone 😀\n')
    check(first['message'] == 'Hello from the espresso machine ✓' and set(first) == {'id', 'message', 'created', 'updated'},
          'add creates a greeting')
    check(second['message'] == 'Café greetings to everyone 😀', 'add - reads the message from standard input')
    check(messages(pc.json('list')) == [second['message'], first['message']], 'list shows the greetings newest first')
    check(messages(pc.json('list', '--limit', '1')) == [second['message']], 'list --limit bounds the result')
    pc.run('list', '--limit', '0', expect=2)
    pc.run('add', '   ', expect=2)
    check('1-500' in pc.run('add', 'x' * 501, expect=2).stderr, 'add refuses empty and over-long messages')

    found = pc.json('search', 'espresso')
    check([g['id'] for g in found['greetings']] == [first['id']] and found['generation'] and not found['hasMore']
          and 'espresso' in found['greetings'][0]['excerpt'], 'full-text search finds the greeting')
    check(pc.json('search', 'café')['greetings'][0]['id'] == second['id'], 'search matches Unicode words')
    check(pc.json('search', 'nonexistentword')['greetings'] == [], 'search without matches is empty')
    for i in range(3):
        pc.json('add', f'Paged hello number {i}')
    page = pc.json('search', 'paged', '--limit', '2')
    check(len(page['greetings']) == 2 and page['hasMore'] and page['nextOffset'] == 2, 'search pages its results')
    rest = pc.json('search', 'paged', '--limit', '2', '--offset', '2', '--generation', page['generation'])
    check(len(rest['greetings']) == 1 and not rest['hasMore'], 'a continuation with the generation returns the rest')
    pc.run('search', 'paged', '--offset', '2', expect=2)
    pc.json('add', 'Paged hello number 3')
    stale = pc.run('search', 'paged', '--limit', '2', '--offset', '2', '--generation', page['generation'], expect=4)
    check('restart at offset 0' in stale.stderr, 'a changed search generation fails with exit 4')

    rows = pc.json('sql', 'SELECT count(*) FROM greetings')['rows']
    check(rows == [[6]], 'sql counts the greetings')
    newest = pc.json('query', 'SELECT message FROM greetings ORDER BY created DESC, id DESC LIMIT 1')['rows']
    check(newest == [['Paged hello number 3']], 'query is an alias of sql')
    for denied in ('SELECT * FROM users', 'SELECT email FROM users', 'SELECT * FROM user_directory', 'SELECT * FROM sqlite_schema', 'SELECT * FROM dbstat', 'SELECT * FROM pragma_table_info(\'greetings\')',
                   "DELETE FROM greetings", "INSERT INTO greetings(message) VALUES ('x')"):
        pc.run('sql', denied, expect=1)
    check(pc.json('sql', 'SELECT count(*) FROM greetings')['rows'] == [[6]], 'the SQL reader refuses credentials, metadata and writes')

    status, _ = pc.http('GET', '/')
    check(status == 200, 'the reader is served')
    status, body = pc.http('GET', '/api/bundle/instance')
    check(status == 200 and json.loads(body).get('app') == 'templatecontext', 'the instance endpoint reports app templatecontext')
    cache = user_cache(pc.home) / 'templatecontext'
    working = [p for p in cache.iterdir() if p.is_dir() and p.name != 'app' and not p.name.startswith('remote-')] if cache.is_dir() else []
    check(len(working) == 1 and any(working[0].iterdir()), f'the working copy lives in the per-machine templatecontext cache: {working}')
    status, _ = pc.http('GET', '/api/bundle/instance', headers={'Host': f'attacker.example:{pc.port}'})
    check(status == 403, 'foreign Host headers are refused')
    status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT 1'})
    check(status == 401, 'SQL requires authentication')
    status, _ = pc.http('POST', '/api/context/search', {'index': 'greetings', 'query': 'hello'})
    check(status == 401, 'search requires authentication')
    status, _ = pc.http('GET', '/api/collections/greetings/records')
    check(status in (200, 401, 403) and b'espresso' not in pc.http('GET', '/api/collections/greetings/records')[1],
          'anonymous record listing returns no greetings')
    status, _ = pc.http('POST', '/api/bundle/token')
    check(status == 403, 'local tokens require the private key')
    status, _ = pc.http('POST', '/api/bundle/login-code')
    check(status == 403, 'sign-in codes require the private key')
    link = pc.json('open', '--no-browser')['signInLink']
    check(link.startswith(pc.url + '/bundle/login#'), 'open prints a single-use sign-in link')
    status, page_html = pc.http('GET', '/bundle/login')
    check(status == 200 and b'TemplateContext sign-in' in page_html, 'the sign-in page is served')
    code = link.split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['id'] == me['id'], 'the sign-in link signs in the account')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the sign-in link is single-use')
    token(pc, PASSWORD)
    check(True, 'the reader password works')

    pc.json('stop')
    names = sorted(p.name for p in pc.project.iterdir())
    check(names in (['templatecontext.db', 'templatecontext.json'], ['storage', 'templatecontext.db', 'templatecontext.json']),
          f'only the project files remain after stop: {names}')
    with closing(sqlite3.connect(f'file:{pc.db.as_posix()}?mode=ro', uri=True)) as db:
        stored = db.execute('SELECT count(*) FROM greetings').fetchone()[0]
    check(stored == 6, 'stop writes the greetings back to the synced database')
    check(len(pc.json('list')) == 6, 'greetings survive a restart')

    # Another computer is using the project.
    pc.json('stop')
    pc.db.with_name('templatecontext.db.lease').write_text(json.dumps({'host': 'other-laptop', 'pid': 1, 'heartbeat': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}))
    blocked = pc.run('ensure-running', expect=1)
    check('other-laptop' in blocked.stderr, 'a fresh lease from another computer blocks start')
    pc.json('ensure-running', '--take-over')
    check(pc.json('status')['running'], '--take-over starts the server')

    skills = pc.root / 'skill'
    installed = pc.json('skill', '--dir', str(skills))
    text = (skills / 'SKILL.md').read_text(encoding='utf-8')
    check(str(pc.binary) in text and str(pc.db) in text and '{{' not in text, 'the skill names the absolute binary and database')
    leftovers = [str(p) for p in skills.rglob('*.md') if '{{' in p.read_text(encoding='utf-8')]
    check(not leftovers and (skills / 'references' / 'commands.md').is_file() and not (skills / 'SKILL.remote.md').exists(),
          f'the installed skill has its references and no placeholders or remote template: {leftovers}')
    check(installed['binary'] == str(pc.binary), 'skill install reports the binary')
    check((skills / '.templatecontext').is_file(), 'the skill directory carries the .templatecontext marker')
    check('name: templatecontext' in text, 'the skill is named templatecontext')
    mentions = [str(p) for p in skills.rglob('*') if p.is_file() and RETIRED in p.read_bytes().decode('utf-8', 'replace').lower()]
    check(not mentions, f'no installed skill file mentions the retired product: {mentions}')
    (pc.home / '.claude').mkdir()
    agent = pc.json('skill', '--agent', 'claude-code')
    target = pc.home / '.claude' / 'skills' / 'templatecontext'
    check(agent['installed'].get('claude-code') == str(target) and (target / 'SKILL.md').is_file()
          and (target / '.templatecontext').is_file(), 'the Claude Code skill is installed as templatecontext')
    every = pc.json('skill')
    check(every['installed']['claude-code'] == str(target) and every['installed']['codex'].startswith('skipped')
          and every['installed']['antigravity'].startswith('skipped'), 'skill skips agents whose configuration folder is missing')


def single_user_scenario(pc):
    """One hidden account reads and writes every greeting; nobody else gets in, and no second account exists."""
    mine = token(pc, PASSWORD)
    me = pc.json('whoami')['id']
    key = local_key(pc)

    # The account is the only one: the server refuses a second, whichever way it is asked for.
    status, body = pc.http('POST', '/api/bundle/account', {'password': 'synthetic second password'},
                           headers={'X-TemplateContext-Local-Key': key})
    check(status == 400 and b'exactly one' in body, f'the server refuses a second account: {status} {body!r}')
    check(token(pc, PASSWORD) and pc.json('whoami')['id'] == me, 'the refused second account changes nothing')
    for headers in ({}, {'Authorization': mine}):
        status, _ = pc.http('POST', '/api/collections/users/records', {'email': 'second@example.localhost', 'password': 'synthetic second password',
                                                                      'passwordConfirm': 'synthetic second password'}, headers=headers)
        check(status in (400, 403), f'the records API creates no account ({"signed in" if headers else "anonymous"})')
    status, body = pc.http('GET', '/api/collections/users/records', headers={'Authorization': mine})
    check(status == 403 or (status == 200 and me not in body.decode()), 'accounts cannot be listed')
    status, _ = pc.http('DELETE', f'/api/collections/users/records/{me}', headers={'Authorization': mine})
    check(status in (403, 404), 'the account cannot be deleted')
    status, _ = pc.http('POST', '/api/bundle/account', {'password': 'synthetic second password'})
    check(status == 403, 'creating the account requires the local key')
    status, _ = pc.http('POST', '/api/bundle/password', {'password': 'synthetic new password'})
    check(status == 403, 'changing the password requires the local key')
    pc.json('flush')
    with closing(sqlite3.connect(f'file:{pc.db.as_posix()}?mode=ro', uri=True)) as db:
        accounts = db.execute('SELECT id, email FROM users').fetchall()
    check(accounts == [(me, ACCOUNT_EMAIL)], f'the database holds exactly one account: {accounts}')

    # Every greeting belongs to the project: the account reads, edits and deletes them all.
    status, body = pc.http('POST', '/api/collections/greetings/records', {'message': 'Through the records API', 'owner': me}, headers={'Authorization': mine})
    created = json.loads(body)
    check(status == 200 and 'owner' not in created and created['message'] == 'Through the records API', 'the records API creates a greeting without an owner')
    status, body = pc.http('GET', '/api/collections/greetings/records?perPage=100', headers={'Authorization': mine})
    listed = {item['id'] for item in json.loads(body)['items']}
    check(status == 200 and created['id'] in listed and len(listed) == pc.json('sql', 'SELECT count(*) FROM greetings')['rows'][0][0],
          'the account lists every greeting through the records API')
    check(created['id'] in json.dumps(pc.json('list')) and [g['id'] for g in pc.json('search', 'records')['greetings']] == [created['id']]
          and pc.json('sql', f"SELECT message FROM greetings WHERE id = '{created['id']}'")['rows'] == [['Through the records API']],
          'list, search and SQL read the greeting')
    status, body = pc.http('PATCH', f"/api/collections/greetings/records/{created['id']}", {'message': 'Edited greeting'}, headers={'Authorization': mine})
    check(status == 200 and json.loads(body)['message'] == 'Edited greeting', 'the account edits a greeting')

    # Authentication stays on for every read and write.
    for headers, who in (({}, 'anonymous'), ({'Authorization': 'not-a-valid-token'}, 'an invalid token')):
        status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT count(*) FROM greetings'}, headers=headers)
        check(status == 401, f'SQL refuses {who}')
        status, _ = pc.http('GET', '/api/context/schema', headers=headers)
        check(status == 401, f'the schema refuses {who}')
        status, _ = pc.http('POST', '/api/context/search', {'index': 'greetings', 'query': 'edited'}, headers=headers)
        check(status == 401, f'search refuses {who}')
        status, body = pc.http('GET', '/api/collections/greetings/records', headers=headers)
        check(status in (200, 401, 403) and b'Edited greeting' not in body, f'record listing shows {who} no greetings')
        status, _ = pc.http('GET', f"/api/collections/greetings/records/{created['id']}", headers=headers)
        check(status in (401, 403, 404), f'reading a greeting refuses {who}')
        status, _ = pc.http('POST', '/api/collections/greetings/records', {'message': 'Intruder'}, headers=headers)
        check(status in (400, 401, 403), f'adding a greeting refuses {who}')
        status, _ = pc.http('PATCH', f"/api/collections/greetings/records/{created['id']}", {'message': 'Hijacked'}, headers=headers)
        check(status in (401, 403, 404), f'editing a greeting refuses {who}')
        status, _ = pc.http('DELETE', f"/api/collections/greetings/records/{created['id']}", headers=headers)
        check(status in (401, 403, 404), f'deleting a greeting refuses {who}')
    check(pc.json('sql', f"SELECT message FROM greetings WHERE id = '{created['id']}'")['rows'] == [['Edited greeting']],
          'refused requests change nothing')
    status, _ = pc.http('DELETE', f"/api/collections/greetings/records/{created['id']}", headers={'Authorization': mine})
    check(status == 204, 'the account deletes a greeting')

    # password changes the reader password and, like any PocketBase password change, revokes earlier tokens.
    new_password = 'synthetic changed password'
    check(pc.json('password', '--password-stdin', stdin=new_password + '\n') == {'password': 'changed'}, 'password changes the reader password')
    status, _ = pc.http('POST', '/api/collections/users/auth-with-password', {'identity': ACCOUNT_EMAIL, 'password': PASSWORD})
    check(status == 400, 'the old password no longer signs in')
    status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT 1'}, headers={'Authorization': mine})
    check(status == 401, 'a password change revokes earlier sessions')
    token(pc, new_password)
    pc.json('password', '--password-stdin', stdin=PASSWORD + '\n')
    token(pc, PASSWORD)
    check(True, 'the password can be changed back')


def earlier_release_scenario(pc):
    """A database created by an earlier, multi-account release is refused before any migration, unchanged."""
    pc.json('flush')
    folder = pc.root / 'Earlier'
    folder.mkdir()
    old = folder / 'templatecontext.db'
    shutil.copyfile(pc.db, old)
    with closing(sqlite3.connect(old)) as db:
        db.execute("INSERT INTO _migrations(file, applied) VALUES ('1800000000_users.js', 1)")
        db.execute('CREATE TABLE user_directory(id TEXT PRIMARY KEY, name TEXT)')
        db.commit()
    port = free_port()
    (folder / 'templatecontext.json').write_text(json.dumps({'port': port}), encoding='utf-8')
    before = hashlib.sha256(old.read_bytes()).hexdigest()
    result = subprocess.run([str(pc.binary), '--db', str(old), 'ensure-running'], capture_output=True, text=True, encoding='utf-8',
                            env=pc.env, timeout=120)
    check(result.returncode == 1 and 'different TemplateContext release' in result.stderr and '1800000000_users.js' in result.stderr,
          f'a database of an earlier release is refused with a clear message: {result.stderr.strip()[-300:]}')
    check(hashlib.sha256(old.read_bytes()).hexdigest() == before and not listening(port), 'the refused database is left unchanged and nothing serves it')
    check(sorted(p.name for p in folder.iterdir()) == ['templatecontext.db', 'templatecontext.json'], 'the refusal writes nothing beside the database')


def server_pid(pc):
    return json.loads(pc.db.with_name('templatecontext.db.lease').read_text(encoding='utf-8'))['pid']


def crash(pc):
    """Kill the server without a write-back, as a power loss would."""
    pid = server_pid(pc)
    os.kill(pid, signal.SIGKILL if hasattr(signal, 'SIGKILL') else signal.SIGTERM)
    for _ in range(100):
        if not listening(pc.port):
            break
        time.sleep(0.1)
    time.sleep(0.5)  # let the OS release the server's lock


def replace_elsewhere(pc, message):
    """Another computer's sync client replaces the synced database with a version holding message."""
    work = pc.root / 'elsewhere'
    work.mkdir(exist_ok=True)
    copy = work / 'copy.db'
    shutil.copyfile(pc.db, copy)
    with closing(sqlite3.connect(copy)) as db:
        record = hashlib.sha256(message.encode()).hexdigest()[:15]
        db.execute("INSERT INTO greetings(id, message, created, updated) VALUES (?, ?, '2026-01-01 00:00:00.000Z', '2026-01-01 00:00:00.000Z')",
                   (record, message))
        db.commit()
        db.execute('PRAGMA journal_mode=DELETE')
    os.replace(copy, pc.db)


def stored_messages(path):
    with closing(sqlite3.connect(f'file:{path.as_posix()}?mode=ro', uri=True)) as db:
        return {row[0] for row in db.execute('SELECT message FROM greetings')}


def sync_scenario(pc):
    """Write-back after a crash, a synced file replaced elsewhere, and both recover choices."""
    pc.json('ensure-running')
    # A crash with unsaved changes: the next start writes them back.
    pc.json('add', 'Survives a crash')
    crash(pc)
    pc.json('ensure-running')
    check('Survives a crash' in messages(pc.json('list')), 'unsaved changes survive a crash')
    pc.json('flush')
    check('Survives a crash' in stored_messages(pc.db), 'the recovered changes reach the synced database')

    # The synced file changes elsewhere while this computer has unsaved changes.
    pc.json('add', 'Local change while running')
    replace_elsewhere(pc, 'Remote change while running')
    conflicts = []
    for _ in range(60):
        conflicts = pc.json('status')['conflicts']
        if conflicts:
            break
        time.sleep(0.5)
    check(len(conflicts) == 1 and Path(conflicts[0]).name.startswith('templatecontext.db.conflict-'), f'a write-back conflict keeps a copy: {conflicts}')
    check(stored_messages(pc.db) >= {'Remote change while running'} and 'Local change while running' not in stored_messages(pc.db),
          'the file written elsewhere is never overwritten')
    check('Local change while running' in stored_messages(Path(conflicts[0])), "the conflict copy holds this computer's change")
    pc.json('stop')
    blocked = pc.run('ensure-running', expect=1)
    check('recover' in blocked.stderr, 'start after a conflict asks for recover')
    pc.run('recover', '--keep', 'both', expect=2)
    pc.json('recover', '--keep', 'synced')
    current = messages(pc.json('list'))
    check('Remote change while running' in current and 'Local change while running' not in current, 'recover --keep synced uses the synced file')
    check('Local change while running' in stored_messages(Path(conflicts[0])), "this computer's work stays in the conflict copy")
    pc.run('recover', '--keep', 'local', expect=1)
    check(True, 'recover refuses while the server runs')

    # A crash with unsaved changes while the synced file also changed: keep local.
    pc.json('add', 'Local change before crash')
    crash(pc)
    replace_elsewhere(pc, 'Remote change after crash')
    blocked = pc.run('ensure-running', expect=1)
    check('recover' in blocked.stderr, 'an interrupted run over a changed synced file needs recover')
    pc.json('recover', '--keep', 'local')
    replaced = list(pc.project.glob('templatecontext.db.replaced-*.db'))
    check(len(replaced) == 1 and 'Remote change after crash' in stored_messages(replaced[0]), 'recover --keep local saves the other file')
    current = messages(pc.json('list'))
    check('Local change before crash' in current and 'Remote change after crash' not in current, "recover --keep local keeps this computer's work")
    check('Local change before crash' in stored_messages(pc.db), 'the kept work is written back')
    pc.json('stop')
    check(not pc.run('recover', '--keep', 'local', expect=1).stdout, 'recover without unsaved changes has nothing to do')
    for path in [*replaced, *map(Path, conflicts)]:
        path.unlink()
    pc.json('ensure-running')


def remote_scenario(pc, r):
    instance = json.loads((pc.project / 'templatecontext.json').read_text(encoding='utf-8'))['instance']
    me = pc.json('whoami')['id']

    missing = r.run('whoami', expect=2)
    check('templatecontext login' in missing.stderr, 'remote data commands require login')
    status = r.json('status')
    check(status['url'] == pc.url and status['reachable'] and not status['loggedIn'], 'remote status reports reachable and logged out')
    r.run('login', '--password-stdin', stdin='wrong password\n', expect=1)
    r.run('login', '--email', ACCOUNT_EMAIL, '--password-stdin', stdin=PASSWORD + '\n', expect=2)
    check(not r.json('status')['loggedIn'], 'remote login refuses a wrong password and has no --email')
    login = r.json('login', '--password-stdin', stdin=PASSWORD + '\n')
    check(login == {'url': pc.url, 'instance': instance}, 'remote login with the password reports the instance')
    session = r.session()
    check(session.parent.name == 'remote-' + hashlib.sha256(pc.url.encode()).hexdigest()[:16], 'the session is keyed by the URL')
    if os.name != 'nt':
        check(session.stat().st_mode & 0o777 == 0o600, 'the remote session is private')
    check(PASSWORD not in session.read_text(encoding='utf-8') and ACCOUNT_EMAIL not in session.read_text(encoding='utf-8'),
          'the session file holds no password or account email')
    status = r.json('status')
    check(status['loggedIn'] and status['instance'] == instance and 'email' not in status, 'remote status reports the session')
    env_status = r.json('status', url=False, env={'TEMPLATECONTEXT_URL': pc.url})
    check(env_status['url'] == pc.url and env_status['loggedIn'], 'TEMPLATECONTEXT_URL selects remote mode')

    remote_me = r.json('whoami')
    check(remote_me == {'id': me, 'url': pc.url}, 'remote whoami reports the same account without a database')
    check(messages(r.json('list')) == messages(pc.json('list')), 'the remote client sees the same greetings')
    added = r.json('add', 'Rooibosquartz greetings from the other computer')
    check(messages(r.json('list'))[0] == 'Rooibosquartz greetings from the other computer', 'remote list shows the remote greeting')
    check([g['id'] for g in r.json('search', 'rooibosquartz')['greetings']] == [added['id']], 'remote search finds the remote greeting')
    check(r.json('sql', 'SELECT count(*) FROM greetings')['rows'] == pc.json('sql', 'SELECT count(*) FROM greetings')['rows'],
          'remote SQL reads the same greetings')
    check([g['id'] for g in pc.json('search', 'rooibosquartz')['greetings']] == [added['id']], 'the local client reads the remote greeting')

    for args in (['stop'], ['ensure-running'], ['start'], ['serve'], ['flush'], ['recover', '--keep', 'local'], ['init'],
                 ['password', '--password-stdin'], ['login-code']):
        refused = r.run(*args, expect=2)
        check('not available with --url' in refused.stderr, f'{args[0]} is refused in remote mode')
    check('unknown command' in r.run('account', expect=2).stderr, 'there is no account command')
    check(pc.json('status')['running'], 'the server keeps running after refused remote commands')
    check(r.run('whoami', '--db', str(pc.db), expect=None).returncode == 2, '--url with --db is refused')
    for url in ('http://example.com:8098', 'http://10.0.0.1:8098', 'https://127.0.0.1:8098'):
        check(r.run('status', url=url, expect=None).returncode == 2, f'non-loopback URL {url} is refused')

    with Forward(pc.port) as forward:
        via = f'http://127.0.0.1:{forward.port}'
        check(http_status(via + '/api/bundle/instance') == 403, 'a forward on a different port fails the Host check')
        check(not r.json('status', url=via)['reachable'], 'remote status through a wrong-port forward is not reachable')
        check(r.run('whoami', url=via, expect=None).returncode == 1, 'remote whoami through a wrong-port forward fails')
        login = r.run('login', '--password-stdin', stdin=PASSWORD + '\n', url=via, expect=None)
        check(login.returncode == 1, 'remote login through a wrong-port forward fails')

    closed = free_port()
    unreachable = r.run('whoami', url=f'http://127.0.0.1:{closed}', expect=1)
    check('not reachable' in unreachable.stderr, 'an unreachable server is reported')
    check(not listening(closed), 'remote mode never starts a server')

    original = session.read_text(encoding='utf-8')
    forged = json.loads(original)
    forged['instance'] = 'f' * len(instance)
    session.write_text(json.dumps(forged), encoding='utf-8')
    other = r.run('whoami', expect=1)
    check('different' in other.stderr and 'sign in again' in other.stderr, 'a session for another instance is refused')
    session.write_text(original, encoding='utf-8')
    check(r.json('whoami')['id'] == me, 'the restored session works again')

    skills = r.root / 'skill'
    r.json('skill', '--dir', str(skills))
    text = (skills / 'SKILL.md').read_text(encoding='utf-8')
    check(pc.url in text and str(r.binary) in text and '--url' in text and '--db' not in text and 'ensure-running' not in text
          and '{{' not in text and '# TemplateContext (remote)' in text and not (skills / 'SKILL.remote.md').exists(),
          'the remote skill names the binary and URL and no local lifecycle')
    opened = r.json('open', '--no-browser')
    check(opened['url'] == pc.url and opened.get('note'), 'remote open prints the server URL')

    check(r.json('logout') == {'url': pc.url, 'loggedOut': True}, 'remote logout ends the session')
    r.run('whoami', expect=2)
    check(not r.json('status')['loggedIn'], 'after logout the remote is logged out')

    leftovers = [p for p in r.root.rglob('*') if p.name in ('templatecontext.db', 'templatecontext.json', 'local.key') or p.suffix in ('.db', '.lease')]
    check(not leftovers, f'remote mode creates no database, project or key: {leftovers}')
    check(sorted(p.name for p in r.work.iterdir()) == [], 'remote mode writes nothing to the working folder')


def tunnel_scenario(pc, r):
    """Offline checks of `tunnel`: its help and every refusal that comes before any network access."""
    help_text = r.run('tunnel', '--help', url=False).stdout
    check('tunnel serve' in help_text and 'tunnel forward' in help_text and '--address-stdin' in help_text, 'tunnel --help documents both sides')
    check('tunnel serve' in r.run('help', url=False).stdout, 'the overview lists the tunnel')
    for args, url in ((['tunnel', 'serve'], None), (['tunnel', 'forward', '--address-stdin', '--db', str(pc.db)], False),
                      (['tunnel', 'forward', '--address-stdin', '--new-address'], None), (['tunnel'], False), (['tunnel', 'open'], False)):
        r.run(*args, url=url, stdin='tcSyntheticButInvalid\n', expect=2)
    check(True, 'tunnel refuses serve with --url, forward with --db, --new-address on forward and unknown subcommands')
    for text in ('tcSyntheticButInvalidSecret', 'not-an-address'):
        refused = r.run('tunnel', 'forward', '--address-stdin', stdin=text + '\n', expect=2)
        check('not a valid tailcat address' in refused.stderr and text not in refused.stderr + refused.stdout,
              'an invalid tunnel address is refused without being echoed')
    refused = r.run('tunnel', 'forward', 'tcSyntheticButInvalidSecret', url=False, expect=2)
    check('tcSyntheticButInvalidSecret' not in refused.stderr, 'an invalid address argument is not echoed')
    missing = r.run('tunnel', 'serve', '--db', str(r.work / 'none.db'), url=False, expect=2)
    check('no TemplateContext database' in missing.stderr and not (r.work / 'none.db').exists(), 'tunnel serve never creates a database')
    check(not list(user_cache(r.home).rglob('tunnel.json')), 'refused tunnel commands keep no tunnel identity')


def codes_scenario(pc, r):
    """One-time sign-in codes for the project's account."""
    # Started first so the 30-second expiry overlaps the other checks.
    expiring = pc.json('login-code', '--ttl', '30')
    expiring_at = time.monotonic()
    check(sorted(expiring) == ['code', 'expires'] and expiring['code'] and expiring['expires'],
          'login-code accepts the minimum lifetime of 30 seconds')
    me = pc.json('whoami')['id']

    issued = pc.json('login-code')
    check(issued['code'] and issued['expires'], 'login-code issues a code')
    instance = json.loads((pc.project / 'templatecontext.json').read_text(encoding='utf-8'))['instance']
    login = r.json('login', '--code-stdin', stdin=issued['code'] + '\n')
    check(login == {'url': pc.url, 'instance': instance}, 'remote login redeems a code')
    check(r.json('whoami')['id'] == me, 'the code signs in the project account')
    check([g['id'] for g in r.json('search', 'rooibosquartz')['greetings']] != [], 'a code session reads the greetings')
    r.json('logout')
    reused = r.run('login', '--code-stdin', stdin=issued['code'] + '\n', expect=1)
    check('used or expired' in reused.stderr, 'a redeemed code cannot be reused')
    unknown = r.run('login', '--code-stdin', stdin='synthetic-unknown-code\n', expect=1)
    check('used or expired' in unknown.stderr, 'an unknown code is refused')
    check(not r.json('status')['loggedIn'], 'a refused code leaves the remote logged out')
    check('no sign-in code' in r.run('login', '--code-stdin', stdin='\n', expect=2).stderr, 'an empty code is a usage error')

    log = Path(pc.json('status')['log'])
    if log.exists():
        check(issued['code'] not in log.read_text(encoding='utf-8', errors='replace'), 'the server log does not contain the code')

    pc.run('login-code', '--email', 'someone@example.localhost', expect=2)
    check(True, 'login-code has no --email')
    for ttl in ('10', '601'):
        pc.run('login-code', '--ttl', ttl, expect=2)
        check(True, f'login-code refuses --ttl {ttl}')

    pc.run('open', '--email', 'someone@example.localhost', '--no-browser', expect=2)
    check(True, 'open has no --email')
    opened = pc.json('open', '--no-browser')
    code = opened['signInLink'].split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['id'] == me, 'the open link signs in the project account')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the open link is single-use')

    r.run('login', '--code-stdin', '--password-stdin', stdin=issued['code'] + '\n', expect=2)
    check(True, 'login refuses --code-stdin with --password-stdin')

    remaining = 31 - (time.monotonic() - expiring_at)
    if remaining > 0:
        time.sleep(remaining)
    expired = r.run('login', '--code-stdin', stdin=expiring['code'] + '\n', expect=1)
    check('used or expired' in expired.stderr, 'an expired code is refused')
    check(not r.json('status')['loggedIn'], 'an expired code leaves the remote logged out')


if __name__ == '__main__':
    try:
        main()
    except AssertionError as error:
        print('FAIL -', error)
        sys.exit(1)
