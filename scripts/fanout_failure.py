#!/usr/bin/env python3
"""Stop and restore shard3 in the explicitly isolated fanout-lab Compose project."""
import argparse
import json
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--url', default='http://localhost:18080')
args = parser.parse_args()
compose = ['docker', 'compose', '-p', 'fanout-lab', '-f', 'compose.yaml', '-f', 'compose.shards.yaml', '-f', 'compose.fanout.yaml']

def status(path):
    try:
        with urlopen(args.url + path, timeout=12) as response:
            return response.status
    except HTTPError as error:
        return error.code

def wait_healthy():
    for _ in range(60):
        try:
            if status('/health') == 200:
                return
        except (OSError, URLError):
            pass
        time.sleep(.5)
    raise RuntimeError('API did not become healthy')

wait_healthy()
with urlopen(args.url + '/metrics', timeout=10) as response:
    if 'cache_backend_lookups_total' in response.read().decode():
        raise RuntimeError('Disable API caching before testing failed-shard point reads')
if status('/links/seed000006') != 200 or status('/links/seed000002') != 200:
    raise RuntimeError('Seed the default consistent-placement fixture before testing')
# `finally` restores the isolated shard if any assertion or request fails.
stopped = False
try:
    stopped = True
    subprocess.run(compose + ['stop', 'shard3'], check=True, stdout=sys.stderr)
    result = {'healthy_shard_point': status('/links/seed000006'),
              'failed_shard_point': status('/links/seed000002'),
              'user_fanout': status('/users/user001/links'),
              'popular_fanout': status('/analytics/popular')}
    expected = {'healthy_shard_point': 200, 'failed_shard_point': 500, 'user_fanout': 500, 'popular_fanout': 500}
    if result != expected:
        raise RuntimeError(f'unexpected failure responses: {result}')
    with urlopen(args.url + '/metrics', timeout=10) as response:
        result['shard3_error_metric_present'] = 'result="error",shard="shard3"' in response.read().decode()
    if not result['shard3_error_metric_present']:
        raise RuntimeError('missing shard3 error metric')
finally:
    if stopped:
        subprocess.run(compose + ['start', 'shard3'], check=True, stdout=sys.stderr)
wait_healthy()
result['recovery'] = {'health': status('/health'), 'previously_failed_point': status('/links/seed000002'),
                      'user_fanout': status('/users/user001/links')}
if any(value != 200 for value in result['recovery'].values()):
    raise RuntimeError(f'failed to recover: {result["recovery"]}')
print(json.dumps(result, indent=2))
