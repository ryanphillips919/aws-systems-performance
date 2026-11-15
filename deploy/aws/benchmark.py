#!/usr/bin/env python3
"""Run a bounded, explicitly authorized k6 workload on the private lab host."""
import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess

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
def ssh(host, command):
    return subprocess.run(['ssh', '-i', a.key, '-o', 'StrictHostKeyChecking=accept-new', 'ec2-user@' + ips[host], command], check=True, capture_output=True, text=True).stdout
meta = {'utc_start': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'mode': a.mode, 'rate': a.rate, 'seconds': a.seconds, 'placement': 'consistent: shard1,shard2,shard3; vnodes=128; partitions=256; version=v1', 'target': 'http://10.42.0.10:8080'}
meta['git_revision'] = subprocess.run(['git', 'rev-parse', 'HEAD'], check=True, capture_output=True, text=True).stdout.strip()
meta['git_status'] = subprocess.run(['git', 'status', '--short'], check=True, capture_output=True, text=True).stdout
(out / 'metadata.json').write_text(json.dumps(meta, indent=2) + '\n')
for host in ['infra', 'api1', 'api2', 'shard1', 'shard2', 'shard3']:
    (out / (host + '-before.txt')).write_text(ssh(host, 'uname -a; uptime; docker version --format "{{.Server.Version}}"; docker stats --no-stream'))
    identity_command = "token=$(curl -fsS -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token) && curl -fsS -H \"X-aws-ec2-metadata-token: $token\" http://169.254.169.254/latest/dynamic/instance-identity/document"
    (out / (host + '-instance.json')).write_text(ssh(host, identity_command))
    (out / (host + '-images.txt')).write_text(ssh(host, 'docker image ls --digests'))
    if host.startswith('shard'):
        (out / (host + '-count.txt')).write_text(ssh(host, "cd ~/lab/deploy/aws && docker compose -f compose.db.yaml exec -T postgres psql -U app -d analytics -Atc 'select count(*) from links'"))
cmd = f'cd ~/lab/deploy/aws && docker compose -f compose.infra.yaml run --rm --no-deps -e BASE_URL=http://10.42.0.10:8080 -e MODE={a.mode} -e RATE={a.rate} -e DURATION={a.seconds}s k6 run --quiet /benchmarks/fanout.js'
(out / 'k6.json').write_text(ssh('infra', cmd))
for host in ['api1', 'api2']:
    private_ip = {'api1': '10.42.0.11', 'api2': '10.42.0.12'}[host]
    (out / (host + '-metrics.txt')).write_text(ssh('infra', f'curl -fsS http://{private_ip}:8000/metrics'))
for host in ['infra', 'api1', 'api2', 'shard1', 'shard2', 'shard3']:
    (out / (host + '-after.txt')).write_text(ssh(host, 'uptime; docker stats --no-stream'))
print('Results:', out)
