#!/usr/bin/env python3
"""Set or reset the bounded lab delay on both API replicas over SSH."""
import argparse
import json
from pathlib import Path
import shlex
import sys

from lab import HOSTS, require_consistent, running_configs, ssh

p = argparse.ArgumentParser()
p.add_argument('--outputs', required=True)
p.add_argument('--key', required=True)
sub = p.add_subparsers(dest='action', required=True)
set_parser = sub.add_parser('set')
set_parser.add_argument('--shard', required=True, choices=['shard1', 'shard2', 'shard3'])
set_parser.add_argument('--ms', required=True, type=int)
sub.add_parser('reset')
a = p.parse_args()
if a.action == 'set' and not 1 <= a.ms <= 5000:
    p.error('--ms must be between 1 and 5000')
ips = json.loads(Path(a.outputs).read_text())['public_ips']['value']
settings = {'FANOUT_LAB_ENABLED': 'false'} if a.action == 'reset' else {
    'FANOUT_LAB_ENABLED': 'true', 'FANOUT_DELAY_SHARD': a.shard,
    'FANOUT_DELAY_MS': str(a.ms),
    'FANOUT_PRIVATE_ALLOWED_IPS': '10.42.0.13,10.42.0.14,10.42.0.15',
}
# Do not transmit or print database credentials. Replace only these four keys.
remote = r'''import json, os, pathlib, tempfile
p = pathlib.Path('.env')
updates = json.loads(os.environ['LAB_UPDATES'])
keys = {'FANOUT_LAB_ENABLED', 'FANOUT_DELAY_SHARD', 'FANOUT_DELAY_MS', 'FANOUT_PRIVATE_ALLOWED_IPS'}
lines = [line for line in p.read_text().splitlines() if line.partition('=')[0] not in keys]
lines.extend(key + '=' + value for key, value in updates.items())
fd, temp = tempfile.mkstemp(prefix='.env-', dir='.')
try:
    with os.fdopen(fd, 'w') as f: f.write('\n'.join(lines) + '\n')
    os.replace(temp, p)
finally:
    if os.path.exists(temp): os.unlink(temp)
'''
command = ('cd ~/lab/deploy/aws && LAB_UPDATES=' + shlex.quote(json.dumps(settings)) +
           ' python3 -c ' + shlex.quote(remote) +
           ' && docker compose -f compose.api.yaml up -d --force-recreate')
try:
    for host in HOSTS:
        print('Updating', host, flush=True)
        ssh(ips, a.key, host, command)
    ssh(ips, a.key, 'infra', 'for url in http://10.42.0.11:8000/health/ready http://10.42.0.12:8000/health/ready; do curl -fsS --retry 20 --retry-delay 3 --retry-all-errors --connect-timeout 2 "$url" >/dev/null || exit 1; done')
    configs = running_configs(ips, a.key)
    require_consistent(configs)
    for k, v in settings.items():
        if configs['api1']['settings'][k] != v:
            raise RuntimeError('running settings do not match requested delay')
    print(json.dumps(configs['api1']['settings'], indent=2))
except (Exception, KeyboardInterrupt) as exc:
    print('Update incomplete; replicas may differ. Run reset on both before benchmarking. ' + str(exc), file=sys.stderr)
    sys.exit(1)
