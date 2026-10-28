#!/usr/bin/env python3
"""Fault-injection tests for updater control flow; never connects to a real server."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
FAKE = r'''#!/usr/bin/env python3
import json,os,pathlib,signal,sys
name=pathlib.Path(sys.argv[0]).name;a=sys.argv[1:];mode=os.getenv('FAIL_AT','');root=pathlib.Path(os.environ['FAKE_ROOT'])
with (root/'trace.jsonl').open('a') as f:f.write(json.dumps([name]+a)+'\n')
if name=='sha256sum':
 if mode=='checksum':sys.exit(44)
 os.execv('/usr/bin/sha256sum',['sha256sum']+a)
elif name=='git':
 if a[:2]==['rev-parse','--git-path']:print(str(root/'.git'/a[2]))
 elif a[:2]==['rev-parse','--verify']:print('a'*40)
 elif a==['rev-parse','HEAD']:print('b'*40)
 elif a and a[0]=='show':print('#!/bin/bash\nexit 0')
 elif a and a[0]=='ls-tree':print('migrations/000021_account_notifications.up.sql')
elif name=='curl':print('ok')
elif name=='docker':
 if a[:4]==['compose','ps','-q','app']:print('OLD_APP')
 elif a[:4]==['compose','ps','-q','db']:print('DB')
 elif a[:3]==['compose','port','app']:print('127.0.0.1:8080')
 elif a[:2]==['compose','config']:print('{"services":{}}')
 elif a and a[0]=='inspect':print('sha256:oldimage')
 elif 'pg_dump' in a:
  if mode=='backup':sys.exit(42)
  if mode=='interrupt':os.kill(os.getppid(),signal.SIGTERM)
  print('fake-dump')
 elif a[:2]==['compose','up']:
  (root/'attempted').touch()
  if mode=='up':sys.exit(43)
 elif 'psql' in a:
  q=a[-1]
  if 'schema_migrations' in q:print('21:true' if mode=='schema' and (root/'attempted').exists() else ('21:false' if (root/'attempted').exists() else '11:false'))
  elif 'to_regclass' in q:print('f')
  elif 'count(*)' in q:print('0')
  else:print('{}')
'''


class UpdaterFailures(unittest.TestCase):
    def run_case(self, mode):
        tmp = tempfile.TemporaryDirectory(prefix='keepet-updater-fault-')
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.backups = root / 'keepet-backups'
        repo = root / 'repo'
        (repo / '.git').mkdir(parents=True)
        (repo / 'scripts').mkdir()
        for file in ('rollback-arvan-trial.sh', 'database-fingerprint.sql'):
            shutil.copyfile(ROOT / 'scripts' / file, repo / 'scripts' / file)
        (repo / 'compose.yaml').write_text('services: {}\n')
        (repo / 'Dockerfile').write_text('FROM fake\n')
        bin_dir = root / 'bin'
        bin_dir.mkdir()
        for name in ('git', 'docker', 'curl', 'sha256sum'):
            f = bin_dir / name
            f.write_text(FAKE)
            f.chmod(0o700)
        env = dict(os.environ, PATH=str(bin_dir) + ':' + os.environ['PATH'], FAKE_ROOT=str(repo), FAIL_AT=mode)
        result = subprocess.run(['bash', str(ROOT / 'scripts/update-arvan-trial.sh'), str(repo), 'a'*40],
                                env=env, text=True, capture_output=True, timeout=15)
        trace = [json.loads(line) for line in (repo / 'trace.jsonl').read_text().splitlines()]
        return result, trace

    def test_backup_failure_resumes_only_unchanged_container(self):
        r, trace = self.run_case('backup')
        self.assertEqual(r.returncode, 42, r.stderr)
        self.assertIn(['docker', 'start', 'OLD_APP'], trace)
        self.assertNotIn(['docker', 'compose', 'up', '-d', '--no-build'], trace)

    def test_incomplete_inventory_never_marks_backup_ready(self):
        r, trace = self.run_case('checksum')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(['docker', 'start', 'OLD_APP'], trace)
        self.assertEqual(list(self.backups.glob('*/backup.ready')), [])
        self.assertNotIn(['docker', 'compose', 'up', '-d', '--no-build'], trace)

    def test_partial_compose_failure_does_not_start_old_code(self):
        r, trace = self.run_case('up')
        self.assertEqual(r.returncode, 43, r.stderr)
        self.assertNotIn(['docker', 'start', 'OLD_APP'], trace)
        self.assertIn(['docker', 'compose', 'stop', 'app'], trace)

    def test_dirty_schema_stops_new_release_even_with_http_health(self):
        r, trace = self.run_case('schema')
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn(['docker', 'start', 'OLD_APP'], trace)
        self.assertIn(['docker', 'compose', 'stop', 'app'], trace)

    def test_interrupted_backup_preserves_failure_status_and_resumes_old_app(self):
        r, trace = self.run_case('interrupt')
        self.assertEqual(r.returncode, 143, r.stderr)
        self.assertIn(['docker', 'start', 'OLD_APP'], trace)

    def test_success_requires_schema_as_well_as_health(self):
        r, trace = self.run_case('')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('Trial update healthy', r.stdout)
        self.assertIn('--restore', r.stdout)
        self.assertNotIn(['docker', 'start', 'OLD_APP'], trace)


if __name__ == '__main__':
    unittest.main(verbosity=2)
