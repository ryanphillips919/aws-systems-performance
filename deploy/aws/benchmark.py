#!/usr/bin/env python3
"""Run a bounded, explicitly authorized k6 workload on the private lab host."""
import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess
import sys

from lab import HOSTS, request_count, require_consistent, running_configs, ssh

p = argparse.ArgumentParser()
p.add_argument('--outputs', required=True)
p.add_argument('--key', required=True)
p.add_argument('--output', required=True, help='new result directory')
p.add_argument('--mode', choices=['point', 'user', 'popular', 'mixed'], default='user')
p.add_argument('--rate', type=int, default=5)
p.add_argument('--seconds', type=int, default=30)
a = p.parse_args()
if os.environ.get('ALLOW_PRIVATE_BENCHMARK') != 'true':
    p.error('set ALLOW_PRIVATE_BENCHMARK=true for this authorized lab')
if not 1 <= a.rate <= 100 or not 1 <= a.seconds <= 300:
    p.error('rate must be 1..100 and seconds 1..300')
ips = json.loads(Path(a.outputs).read_text())['public_ips']['value']
out = Path(a.output)
out.mkdir(mode=0o700, parents=False, exist_ok=False)

# Fail closed if the replicas differ, including cache, placement and delay.
before_config = running_configs(ips, a.key)
(out / 'api-config-before.json').write_text(json.dumps(before_config, indent=2) + '\n')
require_consistent(before_config)
meta = {'utc_start': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'mode': a.mode, 'rate': a.rate, 'seconds': a.seconds, 'target': 'http://nginx:8080 (private Compose network on infra)'}
meta['git_revision'] = subprocess.run(['git', 'rev-parse', 'HEAD'], check=True, capture_output=True, text=True).stdout.strip()
meta['git_status'] = subprocess.run(['git', 'status', '--short'], check=True, capture_output=True, text=True).stdout
(out / 'metadata.json').write_text(json.dumps(meta, indent=2) + '\n')
for host in ['infra', 'api1', 'api2', 'shard1', 'shard2', 'shard3']:
    (out / (host + '-before.txt')).write_text(ssh(ips, a.key, host, 'uname -a; uptime; docker version --format "{{.Server.Version}}"; docker stats --no-stream'))
    identity_command = "token=$(curl -fsS -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token) && curl -fsS -H \"X-aws-ec2-metadata-token: $token\" http://169.254.169.254/latest/dynamic/instance-identity/document"
    (out / (host + '-instance.json')).write_text(ssh(ips, a.key, host, identity_command))
    (out / (host + '-images.txt')).write_text(ssh(ips, a.key, host, 'docker image ls --digests'))
    if host.startswith('shard'):
        (out / (host + '-count.txt')).write_text(ssh(ips, a.key, host, "cd ~/lab/deploy/aws && docker compose -f compose.db.yaml exec -T postgres psql -U app -d analytics -Atc 'select count(*) from links'"))

routes = {'point': '/links/{code}', 'user': '/users/{userId}/links', 'popular': '/analytics/popular'}
selected_routes = list(routes.values()) if a.mode == 'mixed' else [routes[a.mode]]
def read_metrics(phase):
    result = {}
    for host, ip in [('api1', '10.42.0.11'), ('api2', '10.42.0.12')]:
        raw = ssh(ips, a.key, 'infra', f'curl -fsS http://{ip}:8000/metrics')
        (out / (host + '-metrics-' + phase + '.txt')).write_text(raw)
        result[host] = request_count(raw, selected_routes)
    return result

before = read_metrics('before')
cmd = f'cd ~/lab/deploy/aws && docker compose -f compose.infra.yaml run --rm --no-deps -e BASE_URL=http://nginx:8080 -e MODE={a.mode} -e RATE={a.rate} -e DURATION={a.seconds}s k6 run --quiet /benchmarks/fanout.js'
(out / 'k6.json').write_text(ssh(ips, a.key, 'infra', cmd))
meta['utc_end'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
(out / 'metadata.json').write_text(json.dumps(meta, indent=2) + '\n')
after = read_metrics('after')
after_config = running_configs(ips, a.key)
(out / 'api-config-after.json').write_text(json.dumps(after_config, indent=2) + '\n')
deltas = {host: after[host] - before[host] for host in HOSTS}
(out / 'request-distribution.json').write_text(json.dumps({'routes': selected_routes, 'api_request_counter_delta': deltas}, indent=2) + '\n')
for host in ['infra', 'api1', 'api2', 'shard1', 'shard2', 'shard3']:
    (out / (host + '-after.txt')).write_text(ssh(ips, a.key, host, 'uptime; docker stats --no-stream'))
require_consistent(after_config)
if before_config != after_config:
    sys.exit('API container or settings changed during the run; results are not comparable')
if any(value <= 0 for value in deltas.values()):
    sys.exit('No workload requests observed on at least one replica; inspect request-distribution.json')
print('Request counts by replica:', deltas)
print('Results:', out)
