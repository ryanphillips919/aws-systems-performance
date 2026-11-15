# Six-host distributed benchmark lab

This is an isolated, disposable lab. It does not use the existing Terraform root or local Compose files. No deployment or benchmark is implied by the presence of these files. Review the Terraform plan and obtain explicit approval before creating AWS resources or running load.

## Layout and limits

One AZ and one subnet, `10.42.0.0/24`:

| Private IP | Host | Services |
| --- | --- | --- |
| 10.42.0.10 | infra | Nginx, k6, Prometheus, Redis |
| 10.42.0.11–12 | api1, api2 | one Go API each, identical instance type |
| 10.42.0.13–15 | shard1–3 | one PostgreSQL each |

The infrastructure host is shared by the generator, proxy, cache, and monitoring. Watch its CPU and network; its contention can limit measurements. The API hosts do not run those services. All hosts are in one AZ, so this is not highly available. A host failure, an AZ outage, or Redis/infra failure affects the lab; fan-out requests require every shard. API startup requires all shards, even though an already-running API can serve healthy-shard point reads after a failure.

Both APIs use the same ordered placement configuration and 10 pgx connections per shard. `shardseed` initializes the schema and placement metadata once before the API replicas start, then inserts 1,000 deterministic records. Existing records are retained, not reset. Use fresh volumes for comparable datasets. PostgreSQL, Redis, API metrics and Prometheus have no public ingress. Nginx listens on the infrastructure host private IP; k6 runs there. The operator connects by SSH **directly to all six hosts**, each of which permits port 22 only from the specified operator IPv4 /32. There is no control-host-only SSH or jump-host restriction. Each machine has a public IP for bootstrap downloads; restrict egress further if the environment provides a controlled artifact source.

This is an isolated HTTP, PostgreSQL without TLS, and unauthenticated Redis lab; use synthetic data and a trusted dedicated VPC. The PostgreSQL password is generated locally, sent over SSH, and stored in private `.env` files on API and database hosts. It is visible to privileged host and Docker users. The encrypted root volumes are deleted with instances. The provisioning script uses SSH trust-on-first-use: verify host keys independently when your threat model requires it. The public IPs expose only restricted SSH, but patch hosts and minimize their lifetime. Bootstrap downloads Docker Compose from GitHub without a pinned checksum; verify and pin the binary before using this in a higher-trust setting. Pin AMI and image digests for strict reproducibility; image version tags and the default latest AL2023 AMI can change.

Nginx Open Source uses passive connect/error health handling with `max_fails` and `fail_timeout`; it does not perform active HTTP health checks. Check `/health/serving` and `/health/ready` on each API separately. The former allows partial shard availability; the latter requires all shards. Do not infer that Nginx checks those endpoints. Its retry policy does not enable retries of non-idempotent requests.

## Local checks (no AWS changes)

Requirements: Go 1.24+, Docker Compose, Terraform >= 1.7. Terraform initialization downloads its provider; it does not create AWS resources. Provider and image downloads require network access.

```sh
go test ./...
python3 -m py_compile deploy/aws/*.py
python3 -m unittest discover -s deploy/aws -p 'test_*.py'
terraform -chdir=terraform/distributed-lab init -backend=false
terraform -chdir=terraform/distributed-lab fmt -check
terraform -chdir=terraform/distributed-lab validate
```

The services have not been validated on EC2 simply by passing these checks. Confirm bootstrap, network access, health, metrics, and data distribution after an approved deployment. Scripts and configuration assume x86-64 Amazon Linux 2023 and its `ec2-user` account.

## Prepare and review a plan

Set an existing EC2 key pair, a matching private key, region, one AZ in that region, and your current public IPv4 /32. Verify the AWS account and protect state, plans, and output files; they contain infrastructure details. Never put credentials in Terraform variables, state, user data, or Git. This step performs read-only AWS API calls but creates no resources.

```sh
export AWS_PROFILE=YOUR_PROFILE
export AWS_REGION=us-east-1
export TF_VAR_region="$AWS_REGION"
export TF_VAR_availability_zone=us-east-1a
export TF_VAR_key_name=YOUR_EXISTING_KEY_PAIR
export TF_VAR_operator_cidr=YOUR_PUBLIC_IPV4/32
export LAB_KEY="$HOME/.ssh/YOUR_KEY.pem"
aws sts get-caller-identity
terraform -chdir=terraform/distributed-lab init
terraform -chdir=terraform/distributed-lab plan -out=lab.tfplan
terraform -chdir=terraform/distributed-lab show lab.tfplan
```

Optional: set `TF_VAR_ami_id` to a reviewed AL2023 x86-64 AMI. Check instance quotas and AZ capacity. Plan and state are sensitive operational artifacts. The `/32` must match the operator's actual egress IP. Terraform needs permission for EC2, VPC and related network resources; there are no instance IAM roles or managed databases.

## Only after explicit deployment approval

These are future commands, not actions performed by preparing this repository:

```sh
terraform -chdir=terraform/distributed-lab apply lab.tfplan
umask 077
test -d "$HOME/.distributed-benchmark" || mkdir -m 700 "$HOME/.distributed-benchmark"
terraform -chdir=terraform/distributed-lab output -json > "$HOME/.distributed-benchmark/outputs.json"
python3 deploy/aws/deploy.py --outputs "$HOME/.distributed-benchmark/outputs.json" \
  --password-file "$HOME/.distributed-benchmark/db-password" --key "$LAB_KEY"
```

The deploy script generates the password once; keep this file for reruns with existing PostgreSQL volumes. Changing it does not rotate an initialized database password. It transfers source and selected deployment files, waits for cloud-init, starts the three databases, initializes/seeds through api1, then starts both APIs and checks their readiness and the private Nginx endpoint from infra. SSH connection failures are retried while instances boot; cloud-init errors still fail the run. It creates/builds containers and downloads images. For a clean repeated run, do not blindly rerun it over modified data or configurations: it resets the API environment to baseline. Record the exact Git commit and local changes used for the transfer.

Use the public IPs in the protected output file to run the following on the infrastructure host via SSH. Both API replicas must return healthy and Prometheus must show two targets. Check shard counts using `docker compose -f compose.db.yaml exec postgres psql -U app -d analytics -c 'select count(*) from links'` on each shard host. `/health/serving` can be 200 when a shard is down; inspect the response body.

```sh
curl -fsS http://10.42.0.11:8000/health/ready
curl -fsS http://10.42.0.12:8000/health/ready
curl -fsS http://10.42.0.10:8080/health/ready
curl -fsS http://127.0.0.1:9090/api/v1/targets
```

For a browser, tunnel from the operator workstation using the infrastructure host's public IP from the outputs: `ssh -i "$LAB_KEY" -L 8080:10.42.0.10:8080 -L 9090:127.0.0.1:9090 ec2-user@INFRA_PUBLIC_IP`. Prometheus then appears at `http://localhost:9090`. `PUBLIC_BASE_URL` is the private origin; generated short URLs use that origin.

## Benchmark and delay experiment

After separate authorization to load test, run from the operator workstation. The runner creates a new result directory, checks an explicit opt-in, limits rate and duration, runs k6 on infra against Nginx using the private Compose network (`http://nginx:8080`), and captures both API metrics plus host/container statistics before and after. It reads each running API container’s actual cache, shard placement and delay settings without collecting database passwords, requires matching settings, and checks the containers and settings again after the run. A mismatch or restart invalidates the run. It does not create traffic to arbitrary URLs.

```sh
ALLOW_PRIVATE_BENCHMARK=true python3 deploy/aws/benchmark.py \
  --outputs "$HOME/.distributed-benchmark/outputs.json" --key "$LAB_KEY" \
  --output ./my-private-run --mode user --rate 5 --seconds 30
```

Keep results outside Git. The `api-config-before.json` and `api-config-after.json` files record the actual running settings; `request-distribution.json` contains per-replica HTTP counter deltas for the selected workload routes. The runner fails if either replica receives no observed requests. These counters also include any concurrent traffic to those routes, so run in isolation; unequal counts can occur with short runs and connection reuse. Instance identity documents, image digests, shard counts and host stats are captured too. Record EBS sizes and warmup separately. Collect CloudWatch CPU credits as described below. Inspect dropped iterations, achieved rate, errors and generator saturation. Repeated per-run p99s are not a pooled percentile. Run cache disabled for fan-out latency comparisons; Redis is configured but `CACHE_ENABLED=false` initially.

Use the following commands from the operator workstation to apply the same delay to both replicas, then restore the baseline. Do not change settings during a benchmark. Each command recreates the APIs sequentially and verifies their running settings; service is briefly interrupted. If either operation fails, the hosts may differ. The benchmark refuses mismatched settings; rerun `reset` and investigate before proceeding.

```sh
python3 deploy/aws/delay.py --outputs "$HOME/.distributed-benchmark/outputs.json" --key "$LAB_KEY" set --shard shard3 --ms 200
# After the experiment:
python3 deploy/aws/delay.py --outputs "$HOME/.distributed-benchmark/outputs.json" --key "$LAB_KEY" reset
```

Injection affects reads on the selected shard in each API, is bounded at five seconds, and accepts only explicitly listed literal private IPv4 shard endpoints. There is no HTTP control endpoint. Restarting resets process metrics; record that when comparing runs.

### CPU credits

After the run, use the instance IDs in `*-instance.json` and its UTC `metadata.json` window. CloudWatch reports T3 credit metrics at five-minute granularity; extend the window at least five minutes on either side and allow for publication delay. From the operator workstation, for **each of the six instance IDs**, replace the example timestamps and ID:

```sh
aws cloudwatch get-metric-statistics --namespace AWS/EC2 --metric-name CPUCreditBalance \
  --dimensions Name=InstanceId,Value=i-REPLACE --start-time 2026-01-01T00:00:00Z \
  --end-time 2026-01-01T00:30:00Z --period 300 --statistics Average \
  --region "$AWS_REGION" > cpu-credit-balance-i-REPLACE.json
aws cloudwatch get-metric-statistics --namespace AWS/EC2 --metric-name CPUSurplusCreditBalance \
  --dimensions Name=InstanceId,Value=i-REPLACE --start-time 2026-01-01T00:00:00Z \
  --end-time 2026-01-01T00:30:00Z --period 300 --statistics Average \
  --region "$AWS_REGION" > cpu-surplus-balance-i-REPLACE.json
```

Record `CPUCreditUsage` with `--statistics Sum` for the same interval as well. CloudWatch data may arrive after the benchmark; an empty result does not mean zero. Credit balance and usage can affect both benchmark behavior and cost.

For a separately authorized partial-failure demonstration, stop the postgres service on shard3 with `cd ~/lab/deploy/aws && docker compose -f compose.db.yaml stop postgres`. Verify shard3-dependent point reads and complete fan-out requests fail while a point read routed to a healthy shard may succeed, and inspect both health endpoints and metrics. Restart with `docker compose -f compose.db.yaml start postgres`, wait for readiness, and verify recovery. Avoid running writes or parallel experiments during comparison.

## Teardown and verification

Save needed results first. Destroying the stack deletes shard data and Prometheus data. Keep the same account, region, workspace, and variables. Review the destroy plan and obtain explicit destruction approval before applying it:

```sh
aws sts get-caller-identity
terraform -chdir=terraform/distributed-lab workspace show
terraform -chdir=terraform/distributed-lab state list
terraform -chdir=terraform/distributed-lab plan -destroy -out=destroy.tfplan
terraform -chdir=terraform/distributed-lab show destroy.tfplan
# After explicit destruction approval:
terraform -chdir=terraform/distributed-lab apply destroy.tfplan
terraform -chdir=terraform/distributed-lab state list
```

Verify in the correct AWS account and region that the six instances, their EBS volumes and public IPv4 addresses, network interfaces, security groups, subnet, route table, internet gateway and VPC are gone. Inspect separately for snapshots or resources created outside this state, and check delayed billing. Remove the local password, outputs, plans and state only after retaining anything required by your records policy; do not commit or share them.

## Illustrative cost

For eight hours in us-east-1, on-demand Linux, three `t3.medium` hosts at $0.0418/hour, three `t3.small` at $0.0209/hour, six public IPv4 addresses at $0.005/hour, and 120 GB gp3 at $0.08/GB-month prorated over 730 hours: EC2 is about $1.50, public IPv4 $0.24, and EBS $0.11, totaling **about $1.85**. Same-AZ private traffic is assumed free. This excludes internet egress, taxes, extra EBS performance, retained resources, and T3 Unlimited surplus credits. Sustained runs can incur surplus charges and CPU credit behavior can distort measurements. A 24-hour baseline is approximately $5.55 on these assumptions. Check current [EC2](https://aws.amazon.com/ec2/instance-types/t3/), [EBS](https://aws.amazon.com/ebs/pricing/), and [IPv4](https://aws.amazon.com/vpc/pricing/) rates and actual account billing before deployment.
