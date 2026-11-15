#!/usr/bin/env python3
"""Generate the AWS case-study charts from the curated k6 summaries."""
import json
from pathlib import Path

import matplotlib.pyplot as plt
import numpy as np

ROOT = Path(__file__).resolve().parents[1]
RESULTS = ROOT / 'results/aws-2026-11'
OUT = ROOT / 'docs/images'
OUT.mkdir(parents=True, exist_ok=True)


def load(name):
    return json.loads((RESULTS / f'{name}.json').read_text())


def latency(data, key):
    return data['latency_ms'][key]


plt.rcParams.update({'font.size': 10, 'axes.spines.top': False,
                     'axes.spines.right': False, 'savefig.dpi': 160})
colors = ['#2764a5', '#d46a32', '#7547a2']
metrics = [('Average', 'avg'), ('p95', 'p(95)'), ('p99', 'p(99)')]

# Plot each condition separately; do not pool request percentiles or averages.
rates = [20, 40, 60, 80, 100]
runs = [load('baseline-20')] + [load(f'fanout-{rate}') for rate in rates[1:]]
extra = load('fanout-40-first')
fig, ax = plt.subplots(figsize=(8.2, 4.8), layout='constrained')
for (label, key), color in zip(metrics, colors):
    ax.plot(rates, [latency(run, key) for run in runs], marker='o', linewidth=2,
            color=color, label=label)
    ax.scatter([40], [latency(extra, key)], facecolors='white', edgecolors=color,
               s=55, linewidths=1.7, zorder=4)
ax.set(title='AWS fan-out: latency at offered load', xlabel='Offered requests per second',
       ylabel='Latency (ms)', xticks=rates, ylim=(0, 13))
ax.grid(axis='y', alpha=.2)
ax.scatter([], [], facecolors='white', edgecolors='#555', s=55, label='Additional 40 RPS run')
ax.legend(ncol=2, frameon=False, loc='upper right')
fig.savefig(OUT / 'aws-load-ramp.png', bbox_inches='tight')
plt.close(fig)


def paired_plot(names, labels, title, output):
    fig, ax = plt.subplots(figsize=(8.2, 4.8), layout='constrained')
    xs = np.arange(len(metrics))
    width = .33
    for index, (name, label, color) in enumerate(zip(names, labels, colors)):
        data = load(name)
        vals = [latency(data, key) for _, key in metrics]
        bars = ax.bar(xs + (index - .5) * width, vals, width, label=label, color=color)
        ax.bar_label(bars, labels=[f'{v:.2f}' for v in vals], padding=3, fontsize=9)
    ax.set_xticks(xs, [label for label, _ in metrics])
    ax.set_ylabel('Latency (ms)')
    ax.set_title(title)
    ax.set_ylim(0, max(latency(load(name), key) for name in names for _, key in metrics) * 1.23)
    ax.grid(axis='y', alpha=.2)
    ax.set_axisbelow(True)
    ax.legend(frameon=False, loc='upper left', ncol=2)
    fig.savefig(OUT / output, bbox_inches='tight')
    plt.close(fig)


paired_plot(['baseline-20', 'slow-shard-20'], ['Baseline', 'Shard 3: +200 ms'],
            'AWS fan-out: effect of one delayed shard', 'aws-slow-shard.png')
paired_plot(['point-50', 'fanout-50'], ['Point read (one shard)', 'User listing (three shards)'],
            'AWS point read and fan-out workloads', 'aws-point-vs-fanout.png')
