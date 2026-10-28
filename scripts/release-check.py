#!/usr/bin/env python3
"""Local release checks. Creates only new, disposable keepet_release_* databases."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent.parent
SUITES = {
    'approval': ('booking-approval-browser.cjs', None),
    'scheduling': ('exact-scheduling-browser.cjs', 'SCHEDULING_TEST_DATABASE'),
    'fulfillment': ('fulfillment-browser.cjs', 'FULFILLMENT_TEST_DATABASE'),
    'laboratory': ('laboratory-browser.cjs', 'LABORATORY_TEST_DATABASE'),
    'coordination': ('coordination-browser.cjs', 'COORDINATION_TEST_DATABASE'),
    'chat': ('chat-browser.cjs', 'CHAT_TEST_DATABASE'),
    'followups': ('followups-browser.cjs', 'FOLLOWUPS_TEST_DATABASE'),
    'onboarding': ('onboarding-browser.cjs', 'ONBOARDING_TEST_DATABASE'),
    'google': ('google-auth-browser.cjs', None),
    'editorial': ('editorial-browser.cjs', 'EDITORIAL_TEST_DATABASE'),
    'services': ('service-management-browser.cjs', 'SERVICE_MANAGEMENT_TEST_DATABASE'),
    'notifications': ('account-notifications-browser.cjs', 'NOTIFICATIONS_TEST_DATABASE'),
}


def run(cmd, **kw):
    return subprocess.run(cmd, cwd=ROOT, check=True, **kw)


def sql(db, query):
    if not re.fullmatch(r'keepet_release_[a-z_]+_(test|smoke)', db):
        raise ValueError('disposable local database required')
    return subprocess.check_output(['docker', 'exec', 'keepet-db', 'psql', '-U', 'keepet',
                                    '-d', db, '-At', '-v', 'ON_ERROR_STOP=1', '-c', query], text=True).strip()


def database(name):
    run(['docker', 'exec', 'keepet-db', 'createdb', '-U', 'keepet', name], stdout=subprocess.DEVNULL)


def drop(name):
    # Names can only originate from this process; never drop a caller-supplied database.
    if re.fullmatch(r'keepet_release_[a-z_]+_(test|smoke)', name):
        run(['docker', 'exec', 'keepet-db', 'dropdb', '-U', 'keepet', '--if-exists', '--force', name], stdout=subprocess.DEVNULL)


def dsn(name):
    return f'postgres://keepet:keepet_local_only@127.0.0.1:5432/{name}?sslmode=disable'


def start(binary, db, folder, google=False):
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    env = os.environ.copy()
    for key in ('ADMIN_SETTINGS_KEY', 'GOOGLE_CLIENT_ID', 'GOOGLE_CLIENT_SECRET', 'GOOGLE_REDIRECT_URL', 'TRIAGE_CONFIG'):
        env.pop(key, None)
    env.update(APP_MODE='development', DATABASE_URL=dsn(db), PORT=str(port),
               SETTINGS_KEY_FILE=str(folder / 'settings.key'), SESSION_COOKIE_SECURE='false')
    if google:
        env.update(GOOGLE_CLIENT_ID='browser-test-client', GOOGLE_CLIENT_SECRET='local-release-test',
                   GOOGLE_REDIRECT_URL=f'https://127.0.0.1:{port}/auth/google/callback')
    log = (folder / 'server.log').open('w')
    proc = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=log, stderr=log)
    log.close()
    base = f'http://127.0.0.1:{port}'
    try:
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError(f'server exited; inspect {folder / "server.log"}')
            try:
                with urllib.request.urlopen(base + '/health', timeout=1) as res:
                    if res.status == 200 and res.read() == b'ok':
                        return proc, base
            except (OSError, ValueError):
                pass
            time.sleep(0.2)
        raise RuntimeError('local server health timed out')
    except BaseException:
        stop(proc)
        raise


def stop(proc):
    proc.terminate()
    try:
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


def signatures(db):
    # Hash rows, including private blobs, inside PostgreSQL. Never print row contents.
    result = {}
    for table in sql(db, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").splitlines():
        if not re.fullmatch(r'[a-z_]+', table):
            raise ValueError('unexpected table name')
        result[table] = sql(db, f'''SELECT count(*)||':'||COALESCE(md5(string_agg(h,'' ORDER BY h)),'') FROM (SELECT md5(row_to_json(t)::text) h FROM "{table}" t) x''')
    return result


def restore_check(db, restored, folder, binary):
    before = signatures(db)
    dump = folder / 'database.dump'
    with dump.open('wb') as output:
        run(['docker', 'exec', 'keepet-db', 'pg_dump', '-U', 'keepet', '-d', db, '-Fc'], stdout=output)
    if dump.stat().st_size == 0:
        raise RuntimeError('empty dump')
    with dump.open('rb') as source, (folder / 'dump-index.txt').open('w') as output:
        run(['docker', 'exec', '-i', 'keepet-db', 'pg_restore', '-l'], stdin=source, stdout=output)
    database(restored)
    try:
        with dump.open('rb') as source, (folder / 'restore.log').open('w') as output:
            run(['docker', 'exec', '-i', 'keepet-db', 'pg_restore', '-U', 'keepet', '-d', restored,
                 '--exit-on-error', '--no-owner', '--no-privileges'], stdin=source, stdout=output, stderr=output)
        if signatures(restored) != before:
            raise RuntimeError('restored table counts or row digests differ')
        boot = folder / 'restored'
        boot.mkdir()
        if (folder / 'settings.key').exists():
            shutil.copyfile(folder / 'settings.key', boot / 'settings.key')
            os.chmod(boot / 'settings.key', 0o600)
        proc, _ = start(binary, restored, boot)
        try:
            if sql(restored, 'SELECT version::text||\':\'||dirty::text FROM schema_migrations') != '21:false':
                raise RuntimeError('restored application migration state differs')
        finally:
            stop(proc)
        (folder / 'backup.sha256').write_text(hashlib.sha256(dump.read_bytes()).hexdigest() + '  database.dump\n')
        return f'{len(before)} tables: identical counts and row digests; restored app healthy'
    finally:
        drop(restored)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--suite', action='append', choices=SUITES, help='run only selected browser suites, omit Go checks')
    p.add_argument('--output', type=Path, help='new private output directory; defaults to /tmp/keepet-release-*')
    args = p.parse_args()
    os.umask(0o077)
    if args.output:
        args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
        output = args.output.resolve()
    else:
        output = Path(tempfile.mkdtemp(prefix='keepet-release-'))
    # Alphabetic run ID also satisfies the older chat smoke test's database guard.
    run_id = uuid.uuid4().hex[:12].translate(str.maketrans('0123456789', 'ghijklmnop'))
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip())
    results = {'commit': commit, 'working_tree_changes': dirty, 'mode': 'local development only', 'checks': []}
    binary = output / 'keepet'
    print(f'Release evidence: {output}', flush=True)
    def record(name, state, detail=''):
        results['checks'].append({'name': name, 'state': state, 'detail': detail})
        (output / 'report.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n')
        print(f'{state}: {name} {detail}', flush=True)
    try:
        if not args.suite:
            with (output / 'deployment-tests.log').open('w') as log:
                run(['python3', 'scripts/test-deployment.py'], stdout=log, stderr=log)
                run(['bash', '-n', 'scripts/update-arvan-trial.sh', 'scripts/arvan-preflight.sh', 'scripts/rollback-arvan-trial.sh'], stdout=log, stderr=log)
            record('deployment failure paths and shell syntax', 'PASS')
            db = f'keepet_release_{run_id}_test'
            database(db)
            try:
                env = os.environ.copy()
                env['TEST_DATABASE_URL'] = dsn(db)
                with (output / 'go-tests.log').open('w') as log:
                    run(['go', 'test', '-race', './...', '-count=1'], env=env, stdout=log, stderr=log)
                record('Go tests and race detector', 'PASS')
            finally:
                drop(db)
            with (output / 'go-vet.log').open('w') as log:
                run(['go', 'vet', './...'], stdout=log, stderr=log)
            record('go vet', 'PASS')
        run(['go', 'build', '-mod=vendor', '-trimpath', '-o', str(binary), './cmd/server'])
        record('vendored server build', 'PASS')
        # The video editor exercises seeking and ranges using a real tiny MP4.
        if 'editorial' in (args.suite or SUITES):
            run(['ffmpeg', '-loglevel', 'error', '-f', 'lavfi', '-i', 'color=c=green:s=320x180:d=2',
                 '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-movflags', '+faststart', '-y', '/tmp/keepet-editorial-test.mp4'])
        for name in args.suite or SUITES:
            script, dbvar = SUITES[name]
            db = f'keepet_release_{run_id}_{name}_smoke'
            folder = output / name
            folder.mkdir()
            database(db)
            proc = None
            try:
                proc, base = start(binary, db, folder, name == 'google')
                env = os.environ.copy()
                env['BASE_URL'] = base
                if dbvar:
                    env[dbvar] = db
                with (folder / 'browser.log').open('w') as log:
                    run(['node', str(ROOT / 'scripts' / script)], env=env, stdout=log, stderr=log, timeout=240)
                record(name + ' browser', 'PASS')
                stop(proc)
                proc = None
                if name == 'laboratory':
                    detail = restore_check(db, f'keepet_release_{run_id}_restored_smoke', folder, binary)
                    record('PostgreSQL backup and restore rehearsal', 'PASS', detail)
            finally:
                if proc:
                    stop(proc)
                drop(db)
        results['result'] = 'PASS'
    except BaseException as exc:
        record('release run', 'FAIL', str(exc))
        results['result'] = 'FAIL'
        raise
    finally:
        (output / 'report.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n')
        print(f'Report: {output / "report.json"}', flush=True)


if __name__ == '__main__':
    main()
