#!/usr/bin/env python3
"""Read-only verification of seeded records in the isolated fanout-lab stack."""
import json
import subprocess
from urllib.request import urlopen

compose = ['docker', 'compose', '-p', 'fanout-lab', '-f', 'compose.yaml', '-f', 'compose.shards.yaml', '-f', 'compose.fanout.yaml', 'exec', '-T']
base = 'http://localhost:18080'
def sql(shard, query):
    return subprocess.check_output(compose + [shard, 'psql', '-U', 'app', '-d', 'analytics', '-Atc', query], text=True).strip()
def get(path):
    with urlopen(base + path, timeout=10) as response:
        return json.load(response)

health = get('/health')
if health['storage'] != 'sharded-postgres':
    raise RuntimeError(f'unexpected backend: {health}')
counts, codes, users, popular = {}, {}, [], []
for shard in ('shard1', 'shard2', 'shard3'):
    counts[shard] = int(sql(shard, "SELECT count(*) FROM links WHERE code LIKE 'seed%'"))
    code = sql(shard, "SELECT code FROM links WHERE code LIKE 'seed%' ORDER BY code LIMIT 1")
    if not code or get('/links/' + code)['code'] != code:
        raise RuntimeError(f'point lookup failed for {shard}')
    codes[shard] = code
    users += json.loads(sql(shard, "SELECT coalesce(json_agg(t),'[]') FROM (SELECT code,created_at FROM links WHERE user_id='user001' ORDER BY created_at DESC,code LIMIT 50) t"))
    popular += json.loads(sql(shard, "SELECT coalesce(json_agg(t),'[]') FROM (SELECT code,click_count FROM links ORDER BY click_count DESC,code LIMIT 10) t"))
if sum(counts.values()) < 1000 or not all(counts.values()):
    raise RuntimeError(f'expected at least 1000 seeded links across all shards: {counts}')
users.sort(key=lambda item: item['code'])
users.sort(key=lambda item: item['created_at'], reverse=True)
popular.sort(key=lambda item: (-item['click_count'], item['code']))
if [x['code'] for x in get('/users/user001/links')] != [x['code'] for x in users[:50]]:
    raise RuntimeError('user result does not match independent SQL')
if [x['code'] for x in get('/analytics/popular')] != [x['code'] for x in popular[:10]]:
    raise RuntimeError('popular result does not match independent SQL')
with urlopen(base + '/metrics', timeout=10) as response:
    cache_enabled = 'cache_backend_lookups_total' in response.read().decode()
if cache_enabled:
    get('/links/' + codes['shard1']); get('/links/' + codes['shard1'])
    with urlopen(base + '/metrics', timeout=10) as response:
        if 'cache_operations_total{operation="get",result="hit"}' not in response.read().decode():
            raise RuntimeError('expected Redis cache hit was not observed')
print(json.dumps({'physical_counts': counts, 'point_codes': codes, 'user_merge_matches_sql': True,
                  'popular_merge_matches_sql': True, 'cache_hit_checked': cache_enabled}, indent=2))
