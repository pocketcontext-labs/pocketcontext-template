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
            ownership_scenario(pc)
            sync_scenario(pc)
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


def token(pc, email, password):
    status, body = pc.http('POST', '/api/collections/users/auth-with-password', {'identity': email, 'password': password})
    if status != 200:
        raise AssertionError(f'password sign-in for {email}: HTTP {status} {body!r}')
    return json.loads(body)['token']


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

    init = pc.json('init', '--email', 'tester@example.localhost', '--name', 'Tester', '--port', str(pc.port),
                   '--password-stdin', stdin=PASSWORD + '\n')
    check(init['accountCreated'] and init['url'] == pc.url and init['newProject'], 'init creates the account and starts the server')
    check(pc.db.exists(), 'init writes the database to the project folder')
    me = pc.json('whoami')
    check(me['email'] == 'tester@example.localhost' and me['name'] == 'Tester' and me['db'] == str(pc.db), 'whoami reports the local account')
    check(pc.json('list') == [], 'a new project has no greetings')
    schema = pc.json('schema')
    tables = {t['name']: sorted(c['name'] for c in t['columns']) for t in schema['tables']}
    check(tables == {'greetings': ['created', 'id', 'message', 'owner', 'updated'], 'user_directory': ['id', 'name']},
          f'the SQL schema exposes only greetings and user_directory: {tables}')
    check(schema['permissionModel'] == 'filtered-snapshot' and schema['search']['indexes'] == ['greetings']
          and schema['search']['isolation'] == 'private', 'reads use filtered snapshots and a private greetings index')

    first = pc.json('add', 'Hello from the espresso machine ✓')
    second = pc.json('add', '-', stdin='Café greetings to everyone 😀\n')
    check(first['owner'] == me['id'] and first['message'] == 'Hello from the espresso machine ✓' and set(first) == {'id', 'message', 'owner', 'created', 'updated'},
          'add creates a greeting owned by the signed-in account')
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
    joined = pc.json('query', 'SELECT u.name FROM greetings g JOIN user_directory u ON u.id = g.owner LIMIT 1')['rows']
    check(joined == [['Tester']], 'sql joins greetings to the user directory')
    for denied in ('SELECT * FROM users', 'SELECT * FROM sqlite_schema', 'SELECT * FROM dbstat', 'SELECT * FROM pragma_table_info(\'greetings\')',
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
    status, _ = pc.http('POST', '/api/bundle/login-code', {})
    check(status == 403, 'sign-in codes require the private key')
    link = pc.json('open', '--no-browser')['signInLink']
    check(link.startswith(pc.url + '/bundle/login#'), 'open prints a single-use sign-in link')
    status, page_html = pc.http('GET', '/bundle/login')
    check(status == 200 and b'TemplateContext sign-in' in page_html, 'the sign-in page is served')
    code = link.split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['email'] == 'tester@example.localhost', 'the sign-in link signs in')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the sign-in link is single-use')
    token(pc, 'tester@example.localhost', PASSWORD)
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


def ownership_scenario(pc):
    """The server, not the client, decides who owns a greeting; accounts see only their own."""
    other_password = 'synthetic other password'
    other = pc.json('account', '--email', 'other@example.localhost', '--name', 'Other', '--password-stdin', stdin=other_password + '\n')
    mine = token(pc, 'tester@example.localhost', PASSWORD)
    theirs = token(pc, 'other@example.localhost', other_password)
    me = pc.json('whoami')['id']

    status, body = pc.http('POST', '/api/collections/greetings/records', {'message': 'Forged owner', 'owner': me}, headers={'Authorization': theirs})
    created = json.loads(body)
    check(status == 200 and created['owner'] == other['id'], 'create sets the owner to the requester, whatever was sent')
    status, _ = pc.http('POST', '/api/collections/greetings/records', {'message': 'Anonymous'})
    check(status in (400, 401, 403), 'anonymous create is refused')
    status, _ = pc.http('PATCH', f"/api/collections/greetings/records/{created['id']}", {'owner': me}, headers={'Authorization': theirs})
    check(status == 400, 'the owner of a greeting is immutable')
    status, body = pc.http('PATCH', f"/api/collections/greetings/records/{created['id']}", {'message': 'Edited by its owner'}, headers={'Authorization': theirs})
    check(status == 200 and json.loads(body)['message'] == 'Edited by its owner', 'the owner can edit the message')
    status, _ = pc.http('PATCH', f"/api/collections/greetings/records/{created['id']}", {'message': 'Hijacked'}, headers={'Authorization': mine})
    check(status == 404, 'another account cannot edit the greeting')
    status, _ = pc.http('DELETE', f"/api/collections/greetings/records/{created['id']}", headers={'Authorization': mine})
    check(status == 404, 'another account cannot delete the greeting')
    status, body = pc.http('GET', '/api/collections/greetings/records', headers={'Authorization': mine})
    check(status == 200 and created['id'] not in body.decode(), 'record listing shows only own greetings')
    check(created['id'] not in json.dumps(pc.json('list')) and pc.json('search', 'edited')['greetings'] == []
          and pc.json('sql', f"SELECT id FROM greetings WHERE id = '{created['id']}'")['rows'] == [],
          'list, search and SQL never show another account greetings')
    check(pc.json('sql', 'SELECT id, name FROM user_directory')['rows'] == [[me, 'Tester']], 'the user directory shows only the requester')
    status, body = pc.http('POST', '/api/context/search', {'index': 'greetings', 'query': 'edited'}, headers={'Authorization': theirs})
    result = json.loads(body)
    check(status == 200 and [h['id'] for h in result['hits']] == [created['id']] and result['index'] == 'greetings',
          'the search API returns the owner own hits')
    status, _ = pc.http('DELETE', f"/api/collections/greetings/records/{created['id']}", headers={'Authorization': theirs})
    check(status == 204, 'the owner can delete the greeting')
    status, _ = pc.http('DELETE', f"/api/collections/users/records/{other['id']}", headers={'Authorization': theirs})
    check(status in (403, 404), 'accounts cannot be deleted')


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
        owner = db.execute('SELECT id FROM users ORDER BY created LIMIT 1').fetchone()[0]
        record = hashlib.sha256(message.encode()).hexdigest()[:15]
        db.execute("INSERT INTO greetings(id, message, owner, created, updated) VALUES (?, ?, ?, '2026-01-01 00:00:00.000Z', '2026-01-01 00:00:00.000Z')",
                   (record, message, owner))
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
    user_password = 'synthetic user password'
    account = pc.json('account', '--email', 'user@example.localhost', '--name', 'User', '--password-stdin', stdin='first password\n')
    check(account['created'] and account['email'] == 'user@example.localhost', 'account creates another server account')
    again = pc.json('account', '--email', 'user@example.localhost', '--name', 'User', '--password-stdin', stdin=user_password + '\n')
    check(not again['created'] and again['id'] == account['id'], 'account resets the password of an existing account')

    missing = r.run('whoami', expect=2)
    check('templatecontext login' in missing.stderr, 'remote data commands require login')
    status = r.json('status')
    check(status['url'] == pc.url and status['reachable'] and not status['loggedIn'], 'remote status reports reachable and logged out')
    r.run('login', '--email', 'user@example.localhost', '--password-stdin', stdin='wrong password\n', expect=1)
    login = r.json('login', '--email', 'user@example.localhost', '--password-stdin', stdin=user_password + '\n')
    check(login == {'url': pc.url, 'email': 'user@example.localhost', 'instance': instance}, 'remote login reports the instance')
    session = r.session()
    check(session.parent.name == 'remote-' + hashlib.sha256(pc.url.encode()).hexdigest()[:16], 'the session is keyed by the URL')
    if os.name != 'nt':
        check(session.stat().st_mode & 0o777 == 0o600, 'the remote session is private')
    check(PASSWORD not in session.read_text(encoding='utf-8') and user_password not in session.read_text(encoding='utf-8'),
          'the session file holds no password')
    status = r.json('status')
    check(status['loggedIn'] and status['email'] == 'user@example.localhost' and status['instance'] == instance, 'remote status reports the session')
    env_status = r.json('status', url=False, env={'TEMPLATECONTEXT_URL': pc.url})
    check(env_status['url'] == pc.url and env_status['loggedIn'], 'TEMPLATECONTEXT_URL selects remote mode')

    me = r.json('whoami')
    check(me['email'] == 'user@example.localhost' and me['id'] == account['id'] and me['url'] == pc.url and 'db' not in me,
          'remote whoami reports the account without a database')
    check(r.json('list') == [], 'a new remote account sees no greetings of the local account')
    added = r.json('add', 'Rooibosquartz greetings from the other computer')
    check(added['owner'] == account['id'], 'remote add is attributed to the signed-in account')
    check(messages(r.json('list')) == ['Rooibosquartz greetings from the other computer'], 'remote list shows the remote greeting')
    check([g['id'] for g in r.json('search', 'rooibosquartz')['greetings']] == [added['id']], 'remote search finds the remote greeting')
    check(r.json('sql', 'SELECT count(*) FROM greetings')['rows'] == [[1]], 'remote SQL sees only its own greetings')
    check(pc.json('sql', f"SELECT id FROM greetings WHERE id='{added['id']}'")['rows'] == [] and pc.json('search', 'rooibosquartz')['greetings'] == [],
          'the local account cannot read the remote account greetings')

    for args in (['stop'], ['ensure-running'], ['start'], ['serve'], ['flush'], ['recover', '--keep', 'local'], ['init', '--email', 'x@example.localhost'],
                 ['password', '--password-stdin'], ['account', '--email', 'x@example.localhost', '--password-stdin']):
        refused = r.run(*args, expect=2)
        check('not available with --url' in refused.stderr, f'{args[0]} is refused in remote mode')
    check(pc.json('status')['running'], 'the server keeps running after refused remote commands')
    check(r.run('whoami', '--db', str(pc.db), expect=None).returncode == 2, '--url with --db is refused')
    for url in ('http://example.com:8098', 'http://10.0.0.1:8098', 'https://127.0.0.1:8098'):
        check(r.run('status', url=url, expect=None).returncode == 2, f'non-loopback URL {url} is refused')

    with Forward(pc.port) as forward:
        via = f'http://127.0.0.1:{forward.port}'
        check(http_status(via + '/api/bundle/instance') == 403, 'a forward on a different port fails the Host check')
        check(not r.json('status', url=via)['reachable'], 'remote status through a wrong-port forward is not reachable')
        check(r.run('whoami', url=via, expect=None).returncode == 1, 'remote whoami through a wrong-port forward fails')
        login = r.run('login', '--email', 'user@example.localhost', '--password-stdin', stdin=user_password + '\n', url=via, expect=None)
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
    check(r.json('whoami')['id'] == account['id'], 'the restored session works again')

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
    """One-time sign-in codes and accounts without a password."""
    # Started first so the 30-second expiry overlaps the other checks.
    expiring = pc.json('login-code', '--email', 'tester@example.localhost', '--ttl', '30')
    expiring_at = time.monotonic()
    check(expiring['email'] == 'tester@example.localhost' and expiring['code'] and expiring['expires'],
          'login-code accepts the minimum lifetime of 30 seconds')

    coder = pc.json('account', '--email', 'coder@example.localhost', '--name', 'Coder', stdin='')
    check(coder['created'] and coder['email'] == 'coder@example.localhost', 'account without a password creates the account')
    coder_again = pc.json('account', '--email', 'coder@example.localhost', '--name', 'Coder', stdin='')
    check(not coder_again['created'] and coder_again['id'] == coder['id'], 'account without a password is repeatable')
    r.run('login', '--email', 'coder@example.localhost', '--password-stdin', stdin='synthetic guess\n', expect=1)
    check(not r.json('status')['loggedIn'], 'an account created without a password has no known password')

    user = pc.json('account', '--email', 'user@example.localhost', stdin='')
    check(not user['created'], 'account without a password finds the existing account')
    login = r.json('login', '--email', 'user@example.localhost', '--password-stdin', stdin='synthetic user password\n')
    check(login['email'] == 'user@example.localhost', 'account without a password keeps the existing password')
    r.json('logout')

    issued = pc.json('login-code', '--email', 'coder@example.localhost')
    check(issued['email'] == 'coder@example.localhost' and issued['code'] and issued['expires'], 'login-code issues a code')
    instance = json.loads((pc.project / 'templatecontext.json').read_text(encoding='utf-8'))['instance']
    login = r.json('login', '--code-stdin', stdin=issued['code'] + '\n')
    check(login == {'url': pc.url, 'email': 'coder@example.localhost', 'instance': instance}, 'remote login redeems a code')
    me = r.json('whoami')
    check(me['email'] == 'coder@example.localhost' and me['id'] == coder['id'], 'the code signs in its own account')
    check(r.json('sql', 'SELECT id, name FROM user_directory')['rows'] == [[coder['id'], 'Coder']], 'a code session reads only its own directory row')
    check(r.json('list') == [] and r.json('search', 'rooibosquartz')['greetings'] == [],
          'a new code-authenticated account cannot read another account greetings')
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

    pc.run('login-code', '--email', 'nobody@example.localhost', expect=1)
    check(True, 'login-code refuses an unknown email')
    for ttl in ('10', '601'):
        pc.run('login-code', '--email', 'coder@example.localhost', '--ttl', ttl, expect=2)
        check(True, f'login-code refuses --ttl {ttl}')

    opened = pc.json('open', '--email', 'coder@example.localhost', '--no-browser')
    check(opened['signInLink'].startswith(pc.url + '/bundle/login#'), 'open --email prints a sign-in link')
    code = opened['signInLink'].split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['email'] == 'coder@example.localhost', 'open --email signs in that account')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the open --email link is single-use')

    refused = r.run('login-code', '--email', 'coder@example.localhost', expect=2)
    check('not available with --url' in refused.stderr, 'login-code is refused in remote mode')
    r.run('open', '--email', 'coder@example.localhost', '--no-browser', expect=2)
    check(True, 'open --email is refused in remote mode')
    r.run('login', '--code-stdin', '--email', 'coder@example.localhost', stdin=issued['code'] + '\n', expect=2)
    check(True, 'login refuses --code-stdin with --email')

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
