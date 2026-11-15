#!/usr/bin/env python3
"""Plot the sanitized, checked-in AWS fan-out summary."""
import json
from pathlib import Path

import matplotlib.pyplot as plt

root = Path(__file__).resolve().parents[1]
data = json.loads((root / 'experiments/results/aws-fanout-summary.json').read_text())
fig, ax = plt.subplots(figsize=(7.5, 4.4), layout='constrained')
colors = ['#356ab3', '#d06a32']
for i, condition in enumerate(data['conditions']):
    xs = [i * 3, i * 3 + 1]
    vals = [condition['p50_ms'], condition['p95_ms']]
    bars = ax.bar(xs, vals, width=.75, color=colors[i], label=condition['label'])
    ax.bar_label(bars, labels=[f'{v:.1f} ms' for v in vals], padding=4, fontsize=10)
ax.set_xticks([0, 1, 3, 4], ['p50', 'p95', 'p50', 'p95'])
ax.set_ylabel('User-list request latency (ms)')
ax.set_title('AWS fan-out smoke test: one slow shard')
ax.set_ylim(0, 230)
ax.spines[['top', 'right']].set_visible(False)
ax.legend(frameon=False, loc='upper left')
fig.savefig(root / 'experiments/figures/aws-fanout-smoke.png', dpi=160)
