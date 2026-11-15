# Images

The load-ramp, slow-shard, and point-versus-fan-out benchmark charts were generated from the curated k6 summaries with `scripts/plot_aws_results.py`. See the [benchmark methodology](../../results/aws-2026-11/README.md) for regeneration instructions and limitations.

`aws-ec2-redacted.png` and `prometheus-targets-redacted.png` are cropped historical screenshots from the lab. EC2 instance IDs, public DNS, account details, and Prometheus endpoint addresses and labels were cropped or obscured. They show a historical deployment, which has since been destroyed.

`prometheus-shard-activity.png` is a historical Prometheus query view from the lab, showing the rate of list operations grouped by shard.
