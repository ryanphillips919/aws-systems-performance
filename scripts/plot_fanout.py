#!/usr/bin/env python3
"""Generate four PNG charts from the checked-in fan-out experiment JSON."""
import json
import statistics
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import numpy as np

root = Path(__file__).resolve().parent.parent
results = root / 'experiments' / 'results'
out = root / 'experiments' / 'figures'
out.mkdir(exist_ok=True)

def read(name):
    return json.loads((results / name).read_text())

def stat(phase, mode, percentile):
    return statistics.median(read(f'fanout-{phase}-{mode}-{i}.json')['latency_ms'][percentile] for i in (1, 2))

plt.style.use('seaborn-v0_8-whitegrid')
fig, ax = plt.subplots(figsize=(7.5, 4.3), layout='constrained')
labels = ['p50', 'p95', 'p99']
for mode in ('point', 'user', 'popular'):
    ax.plot(labels, [stat('baseline', mode, f'p({p})') for p in (50, 95, 99)], marker='o', label=mode)
ax.set(title='Baseline request latency percentiles', ylabel='Client latency (ms)', xlabel='Percentile')
ax.legend(title='Endpoint'); fig.savefig(out / 'fanout-latency-percentiles.png', dpi=150); plt.close(fig)

fig, ax = plt.subplots(figsize=(7.5, 4.3), layout='constrained')
modes = ['point', 'user', 'popular']; x = np.arange(len(modes)); width = .35
for offset, phase in ((-width/2, 'baseline'), (width/2, 'slow')):
    ax.bar(x + offset, [stat(phase, mode, 'p(95)') for mode in modes], width, label=phase)
ax.set(xticks=x, xticklabels=modes, ylabel='Client p95 (ms)', title='Baseline vs 200 ms delay on shard 3'); ax.legend()
fig.savefig(out / 'fanout-slow-shard.png', dpi=150); plt.close(fig)

sim = read('fanout-probability-simulation.json'); points = sim['points']
fig, ax = plt.subplots(figsize=(7.5, 4.3), layout='constrained')
ax.plot([p['shards'] for p in points], [p['theoretical'] for p in points], marker='o', label='Theory: pⁿ')
ax.plot([p['shards'] for p in points], [p['simulated'] for p in points], marker='x', linestyle='--', label='Independent simulation')
ax.set(xlabel='Shards required', ylabel='Probability all meet threshold', title=f"Independent completion, p={sim['p']}")
ax.set_ylim(0, 1.02); ax.legend(); fig.savefig(out / 'fanout-probability.png', dpi=150); plt.close(fig)

fig, ax = plt.subplots(figsize=(7.5, 4.3), layout='constrained')
names = ['shard1', 'shard2', 'shard3', 'fan-out']; x = np.arange(len(names))
for offset, phase in ((-width/2, 'baseline'), (width/2, 'slow')):
    metric = read(f'fanout-{phase}-metrics.json')['operations']['list']
    values = [metric['shards'][name]['mean_ms'] for name in names[:3]] + [metric['fanout_mean_ms']]
    ax.bar(x + offset, values, width, label=phase)
ax.set(xticks=x, xticklabels=names, ylabel='Server mean duration (ms)', title='Per-shard vs fan-out: user listings'); ax.legend()
fig.savefig(out / 'fanout-shard-vs-overall.png', dpi=150); plt.close(fig)
print(f'Wrote four charts to {out}')
