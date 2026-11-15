"""Shared SSH and running-configuration checks for the private AWS lab."""
import json
import subprocess
import shlex

HOSTS = ('api1', 'api2')


def ssh(ips, key, host, command, data=None):
    return subprocess.run(
        ['ssh', '-i', key, '-o', 'StrictHostKeyChecking=accept-new',
         'ec2-user@' + ips[host], command],
        input=data, check=True, capture_output=True, text=True,
    ).stdout


# Filter on the remote host, before anything containing database passwords is
# returned to the operator. Read Docker's actual container environment rather
# than the potentially stale .env file.
REMOTE_CONFIG = r'''import json, subprocess
from urllib.parse import urlsplit
base = ['docker', 'compose', '-f', 'compose.api.yaml']
cid = subprocess.check_output(base + ['ps', '-q', 'api'], text=True).strip()
if not cid or '\n' in cid: raise SystemExit('expected exactly one running API container')
info = json.loads(subprocess.check_output(['docker', 'inspect', cid], text=True))[0]
if not info['State']['Running']: raise SystemExit('API container is not running')
env = dict(item.split('=', 1) for item in info['Config']['Env'] if '=' in item)
def get(key, default=''): return env.get(key) or default
keys = {
 'STORAGE_BACKEND': 'postgres', 'SHARD_IDS': 'shard1,shard2,shard3',
 'SHARD_STRATEGY': 'consistent', 'SHARD_VNODES': '128', 'SHARD_PARTITIONS': '256',
 'SHARD_MAP_VERSION': 'v1', 'CACHE_ENABLED': 'false', 'CACHE_TTL_SECONDS': '60',
 'CACHE_COALESCE': 'false', 'REDIS_ADDR': 'localhost:6379', 'REDIS_TLS': 'false',
 'FANOUT_LAB_ENABLED': 'false', 'FANOUT_DELAY_SHARD': '', 'FANOUT_DELAY_MS': '0',
 'FANOUT_PRIVATE_ALLOWED_IPS': '',
}
settings = {key: get(key, default) for key, default in keys.items()}
settings['shard_endpoints'] = [urlsplit(url).hostname + ':' + str(urlsplit(url).port or 5432) for url in get('SHARD_DATABASE_URLS').split(',')]
print(json.dumps({'container_id': cid, 'started_at': info['State']['StartedAt'], 'settings': settings}))
'''


def running_configs(ips, key):
    return {host: json.loads(ssh(ips, key, host,
            'cd ~/lab/deploy/aws && python3 -c ' + shlex.quote(REMOTE_CONFIG)))
            for host in HOSTS}


def require_consistent(configs):
    if configs['api1']['settings'] != configs['api2']['settings']:
        raise RuntimeError('API replicas have different running settings; reset or synchronize both before benchmarking')


def request_count(raw, selected_routes):
    """Count completed GET requests for the benchmark routes in a metrics snapshot."""
    total = 0.0
    for line in raw.splitlines():
        if not line.startswith('http_requests_total{'):
            continue
        labels, value = line.split(' ', 1)
        if 'method="GET"' in labels and any('route="' + route + '"' in labels for route in selected_routes):
            total += float(value.split()[0])
    return total
