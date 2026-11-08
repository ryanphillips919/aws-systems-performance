#!/usr/bin/env python3
"""Summarize cumulative Prometheus histograms from an isolated API process."""
import argparse
import json
import re
from urllib.request import urlopen

parser = argparse.ArgumentParser()
parser.add_argument('--url', default='http://localhost:18080/metrics')
args = parser.parse_args()
with urlopen(args.url, timeout=10) as response:
    lines = response.read().decode().splitlines()
values = {}
pattern = re.compile(r'^(\w+)\{([^}]*)\}\s+([0-9.eE+-]+)$')
for line in lines:
    match = pattern.match(line)
    if not match:
        continue
    name, raw_labels, value = match.groups()
    labels = dict(re.findall(r'(\w+)="([^"]*)"', raw_labels))
    if name.startswith(('shard_operation_duration_seconds_', 'shard_fanout_duration_seconds_', 'shard_fanout_shards_contacted_')):
        if name.endswith(('_sum', '_count')):
            values[(name, labels.get('operation'), labels.get('shard'))] = float(value)
output = {'note': 'cumulative metrics since API startup; timings include local injected delays', 'operations': {}}
for operation in ('list', 'popular'):
    overall_count = values.get(('shard_fanout_duration_seconds_count', operation, None), 0)
    if not overall_count:
        continue
    data = {'fanout_count': int(overall_count),
            'fanout_mean_ms': 1000 * values[('shard_fanout_duration_seconds_sum', operation, None)] / overall_count,
            'mean_shards_contacted': values.get(('shard_fanout_shards_contacted_sum', operation, None), 0) / overall_count,
            'shards': {}}
    for shard in ('shard1', 'shard2', 'shard3'):
        count = values.get(('shard_operation_duration_seconds_count', operation, shard), 0)
        if count:
            data['shards'][shard] = {'count': int(count), 'mean_ms': 1000 * values[('shard_operation_duration_seconds_sum', operation, shard)] / count}
    output['operations'][operation] = data
print(json.dumps(output, indent=2))
