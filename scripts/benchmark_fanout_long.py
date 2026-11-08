#!/usr/bin/env python3
"""Run repeated, bounded k6 trials against the isolated local fanout-lab project."""
import argparse
import datetime
import json
import os
import pathlib
import platform
import subprocess
import time
import urllib.error
import urllib.request

COMPOSE = ['docker', 'compose', '-p', 'fanout-lab', '-f', 'compose.yaml', '-f', 'compose.shards.yaml', '-f', 'compose.fanout.yaml']
p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--rate', type=int, default=25, help='offered requests/second (1–100)')
p.add_argument('--duration', type=int, default=60, help='measured seconds per run (10–600)')
p.add_argument('--warmup', type=int, default=10, help='warmup seconds before each run (1–120)')
p.add_argument('--repetitions', type=int, default=3, help='runs per workload and condition (1–10)')
p.add_argument('--output', required=True, help='new output directory; never overwrites existing results')
args = p.parse_args()
for name, value, low, high in [('rate', args.rate, 1, 100), ('duration', args.duration, 10, 600), ('warmup', args.warmup, 1, 120), ('repetitions', args.repetitions, 1, 10)]:
    if not low <= value <= high:
        p.error(f'{name} must be between {low} and {high}')
output = pathlib.Path(args.output)
output.mkdir(parents=True, exist_ok=False)
env = os.environ.copy()
# Pin the comparison's placement and cache, regardless of the caller's environment.
env.update(CACHE_ENABLED='false', SHARD_STRATEGY='consistent', SHARD_VNODES='128', SHARD_MAP_VERSION='v1', FANOUT_HTTP_PORT='18080')

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

local_http = urllib.request.build_opener(NoRedirect())

def run(command, **kwargs):
    return subprocess.run(command, check=True, env=env, text=True, **kwargs)

def wait_health():
    for _ in range(90):
        try:
            with local_http.open('http://127.0.0.1:18080/health/ready', timeout=3) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(1)
    raise RuntimeError('API did not become ready')

def set_condition(slow):
    env.update(FANOUT_LAB_ENABLED='true' if slow else 'false', FANOUT_DELAY_SHARD='shard3' if slow else '', FANOUT_DELAY_MS='200' if slow else '0')
    run(COMPOSE + ['up', '-d', '--no-deps', 'api'])
    wait_health()

def trial(mode, seconds):
    cmd = COMPOSE + ['run', '--rm', '--no-deps', '-e', 'BASE_URL=http://api:8000', '-e', f'MODE={mode}', '-e', f'RATE={args.rate}', '-e', f'DURATION={seconds}s', 'k6', 'run', '--quiet', '/benchmarks/fanout.js']
    return json.loads(run(cmd, capture_output=True).stdout)

try:
    set_condition(False)
    counts = {}
    for shard in ('shard1', 'shard2', 'shard3'):
        counts[shard] = int(run(COMPOSE + ['exec', '-T', shard, 'psql', '-U', 'app', '-d', 'analytics', '-Atc', 'SELECT count(*) FROM links'], capture_output=True).stdout.strip())
    # Refuse to benchmark an absent fixture or misrouted default point lookup.
    for path in ('/links/seed000006', '/users/user001/links?limit=50'):
        with local_http.open('http://127.0.0.1:18080' + path, timeout=5) as response:
            body = json.load(response)
            if path.startswith('/users/') and not body:
                raise RuntimeError('user001 has no fixture records; seed the local project first')
    metadata = {'utc_started': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'platform': platform.platform(), 'architecture': platform.machine(), 'processor': platform.processor(), 'k6_image': 'grafana/k6:0.54.0', 'logical_cpus': os.cpu_count(), 'docker_version': run(['docker', 'version', '--format', '{{.Server.Version}}'], capture_output=True).stdout.strip(), 'dataset_counts': counts, 'dataset_total': sum(counts.values()), 'cache': 'disabled', 'placement': 'consistent, 128 virtual nodes, v1', 'point_code': 'seed000006', 'user': 'user001', 'slow_condition': '200 ms on shard3 reads', 'rate': args.rate, 'duration_seconds': args.duration, 'warmup_seconds': args.warmup, 'repetitions': args.repetitions, 'note': 'Per-run percentiles are not pooled. Dropped iterations or achieved throughput below offered load may indicate generator or system saturation.'}
    (output / 'metadata.json').write_text(json.dumps(metadata, indent=2) + '\n')
    for condition in ('baseline', 'slow'):
        set_condition(condition == 'slow')
        for mode in ('point', 'user'):
            for n in range(1, args.repetitions + 1):
                warmup = trial(mode, args.warmup)
                result = trial(mode, args.duration)
                (output / f'{condition}-{mode}-{n}.json').write_text(json.dumps({'warmup': warmup, 'measurement': result}, indent=2) + '\n')
                dropped = (result.get('dropped_iterations') or {}).get('count', 0)
                achieved = result.get('achieved_requests_per_second', 0)
                if dropped or achieved < args.rate * 0.95:
                    print(f'WARNING: {condition} {mode} run {n}: dropped iterations or achieved throughput below 95% of offered load; investigate generator and system saturation.', flush=True)
                print(f'{condition} {mode} run {n}: {json.dumps(result)}', flush=True)
finally:
    # Restore normal cache-disabled service without touching volumes or results.
    set_condition(False)
