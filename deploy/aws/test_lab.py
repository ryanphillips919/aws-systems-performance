import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from lab import REMOTE_CONFIG, request_count, require_consistent


class LabChecks(unittest.TestCase):
    def test_replicas_must_match(self):
        configs = {'api1': {'settings': {'FANOUT_DELAY_MS': '200'}},
                   'api2': {'settings': {'FANOUT_DELAY_MS': '200'}}}
        require_consistent(configs)
        configs['api2']['settings']['FANOUT_DELAY_MS'] = '0'
        with self.assertRaises(RuntimeError):
            require_consistent(configs)

    def test_counts_only_selected_routes_and_gets(self):
        metrics = '''http_requests_total{method="GET",route="/links/{code}",status="200"} 7
http_requests_total{method="GET",route="/links/{code}",status="503"} 2
http_requests_total{method="POST",route="/links/{code}",status="200"} 5
http_requests_total{method="GET",route="/health/ready",status="200"} 9
'''
        self.assertEqual(request_count(metrics, ['/links/{code}']), 9)

    def test_remote_inspection_redacts_dsn_and_captures_runtime(self):
        with tempfile.TemporaryDirectory() as directory:
            fake = Path(directory) / 'docker'
            payload = [{'State': {'Running': True, 'StartedAt': 'now'}, 'Config': {'Env': [
                'SHARD_DATABASE_URLS=postgresql://app:secret@10.42.0.13:5432/analytics',
                'CACHE_ENABLED=true', 'FANOUT_DELAY_MS=200']}}]
            fake.write_text('#!/bin/sh\ncase "$1" in\ncompose) echo container123;;\ninspect) printf %s ' + repr(json.dumps(payload)) + ';;\nesac\n')
            fake.chmod(0o755)
            env = dict(os.environ, PATH=directory + os.pathsep + os.environ['PATH'])
            result = subprocess.check_output([sys.executable, '-c', REMOTE_CONFIG], env=env, text=True)
            self.assertNotIn('secret', result)
            actual = json.loads(result)
            self.assertEqual(actual['settings']['CACHE_ENABLED'], 'true')
            self.assertEqual(actual['settings']['shard_endpoints'], ['10.42.0.13:5432'])


if __name__ == '__main__':
    unittest.main()
