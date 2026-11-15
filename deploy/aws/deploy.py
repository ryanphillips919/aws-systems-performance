#!/usr/bin/env python3
"""Provision the already-created six-host lab over SSH. Never calls AWS."""
import argparse
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[2]
HOSTS = ['infra', 'api1', 'api2', 'shard1', 'shard2', 'shard3']
PRIVATE = dict(zip(HOSTS, [f'10.42.0.{i}' for i in range(10, 16)]))
p = argparse.ArgumentParser()
p.add_argument('--outputs', required=True, help='terraform output -json file')
p.add_argument('--key', required=True, help='EC2 SSH private key')
p.add_argument('--password-file', required=True, help='private local file, created on first run and retained for reruns')
a = p.parse_args()
outputs = json.loads(Path(a.outputs).read_text())
ips = outputs['public_ips']['value']
if set(ips) != set(HOSTS):
    raise SystemExit('expected exactly the six distributed-lab hosts')
secret_path = Path(a.password_file)
try:
    fd = os.open(secret_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
except FileExistsError:
    if secret_path.stat().st_mode & 0o077:
        raise SystemExit('password file must not be accessible to group or others')
    password = secret_path.read_text().strip()
else:
    password = secrets.token_hex(24)
    with os.fdopen(fd, 'w') as f:
        f.write(password + '\n')
if not password or not all(c in '0123456789abcdef' for c in password):
    raise SystemExit('password must be nonempty lowercase hex')

def ssh(host, command, data=None):
    subprocess.run(['ssh', '-i', a.key, '-o', 'StrictHostKeyChecking=accept-new', 'ec2-user@' + ips[host], command], input=data, check=True)

def env_for(host):
    common = f'HOST_IP={PRIVATE[host]}\n'
    if host.startswith('shard'):
        return common + f'POSTGRES_USER=app\nPOSTGRES_DB=analytics\nPOSTGRES_PASSWORD={password}\n'
    if host.startswith('api'):
        urls = ','.join(f'postgresql://app:{password}@{PRIVATE[x]}:5432/analytics?sslmode=disable&pool_max_conns=10' for x in HOSTS[3:])
        return common + f'''STORAGE_BACKEND=sharded-postgres
SHARD_IDS=shard1,shard2,shard3
SHARD_DATABASE_URLS={urls}
SHARD_STRATEGY=consistent
SHARD_VNODES=128
SHARD_PARTITIONS=256
SHARD_MAP_VERSION=v1
PUBLIC_BASE_URL=http://10.42.0.10:8080
CACHE_ENABLED=false
REDIS_ADDR=10.42.0.10:6379
FANOUT_LAB_ENABLED=false
'''
    return common

# Transfer only source and deployment inputs; no .env, Terraform state, or Git data.
buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode='w:gz') as tar:
    for name in ['Dockerfile', '.dockerignore', 'go.mod', 'go.sum', 'cmd', 'internal', 'benchmarks', 'deploy/aws/compose.db.yaml', 'deploy/aws/compose.api.yaml', 'deploy/aws/compose.infra.yaml', 'deploy/aws/nginx.conf', 'deploy/aws/prometheus.yml']:
        def include(info):
            if Path(info.name).name in {'.env', '__pycache__'}:
                return None
            return info
        tar.add(ROOT / name, arcname=name, filter=include)
archive = buf.getvalue()
for host in HOSTS:
    print('Preparing', host, flush=True)
    ssh(host, 'cloud-init status --wait >/dev/null && mkdir -p ~/lab && tar xzf - -C ~/lab', archive)
    ssh(host, 'umask 077; cat > ~/lab/deploy/aws/.env', env_for(host).encode())
for host in HOSTS[3:]:
    ssh(host, 'cd ~/lab/deploy/aws && docker compose -f compose.db.yaml up -d --wait')
ssh('infra', 'cd ~/lab/deploy/aws && docker compose -f compose.infra.yaml up -d')
ssh('api1', 'cd ~/lab/deploy/aws && docker compose -f compose.api.yaml build && docker compose -f compose.api.yaml run --rm --no-deps api shardseed -count 1000')
for host in ['api1', 'api2']:
    ssh(host, 'cd ~/lab/deploy/aws && docker compose -f compose.api.yaml up -d --build')
print('Started. Verify both API health endpoints from infra before benchmarking.')
