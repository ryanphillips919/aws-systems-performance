# AWS deployment experiment

This is a separate, opt-in deployment of the existing Go API. It does not modify the local Compose stacks. **No AWS deployment, AWS load test, or destruction has been performed by preparing these files.** Obtain explicit approval and review an account-specific plan and cost estimate before applying, load testing, or destroying resources.

## Design and limitations

- An internet-facing HTTP ALB occupies two public subnets. Its listener accepts only `allowed_cidr`; use your own public IPv4 `/32`. Two x86-64 Amazon Linux 2023 instances occupy private subnets in separate availability zones, without public addresses, SSH, inbound administration, or NAT. Only the ALB security group can reach their port 8000.
- One private, encrypted, single-AZ PostgreSQL RDS instance is authoritative. Only the application security group can reach it. Connections use TLS with `verify-full` and Amazon's RDS CA bundle. There is no public database access. RDS manages its password in Secrets Manager; the instance role can read only that secret and the two artifact objects. The app currently uses the RDS master user because it creates its own schema; a separate least-privilege application account and migration process are appropriate before production use.
- Optional `enable_redis=true` adds one private shared ElastiCache Redis node with encryption at rest and in transit. Its security group accepts only the application group. The client uses TLS; this demonstration relies on network isolation and does not configure Redis AUTH. A node failure is tolerated as best-effort caching, though cached values can be stale as described in the main README.
- A free S3 gateway endpoint supplies application artifacts; one Secrets Manager interface endpoint supplies credentials without Internet or NAT access. The single interface endpoint, RDS instance, and optional cache are availability bottlenecks. Cross-AZ endpoint traffic may be billed. The instance role has no SSH, Systems Manager, S3 write, or other broad managed policies. EC2 requires IMDSv2, EBS root volumes are encrypted, and no key pair is installed.
- The AL2023 standard AMI supplies AWS CLI and Python. Bootstrap downloads the binary and CA, starts systemd as an unprivileged user, and refreshes the RDS-managed secret each minute. It restarts the API when the secret changes. Runtime credentials live in a root-only file under `/run`; they are not Terraform variables, user data, S3 objects, or intended outputs. Existing database connections may fail temporarily during rotation; the service refreshes within about a minute when the endpoint is reachable.
- The ALB checks `/health/serving`, which verifies the authoritative database in this single-database configuration. `/health/live` and `/health/ready` remain available. ALB can fail open when all registered targets are unhealthy, so this health check does not ensure every request succeeds. There is no WAF, TLS listener, domain, or application authentication: this is an HTTP-only, restricted-CIDR experiment. Do not send credentials or sensitive link data over it. `/metrics` and the other API endpoints are reachable from that same CIDR.
- This is not a production HA design: no RDS Multi-AZ, database backups, final snapshot, automatic cache failover, scaling, central logs, deployment rollback, or administrative access. **Destroying the stack deletes the database and its data.** CA rotation, application patching, backups, authentication, and a proper HTTPS listener need an operational design before production.

## Prerequisites and reproducibility

Use Terraform >= 1.7, Go 1.24+, curl, an AWS CLI with separately configured short-lived credentials, and k6 for the optional later benchmark. The Terraform operator needs the permissions to manage the listed EC2/VPC, ELB, RDS, Secrets Manager integration, ElastiCache (if enabled), IAM role/profile and policy, and S3 resources. The role passed to EC2 needs `iam:PassRole`; RDS/ElastiCache may require service-linked roles. Check account quotas, permissions, regional offerings, and VPC CIDR overlaps first. Never store credentials in this repository or tfvars.

The provider lock file pins provider releases. The default AMI lookup selects the latest official x86-64 AL2023 image, so repeat plans may select a newer AMI; pass `-var='ami_id=ami-...'` to pin the reviewed image. RDS and Redis use major/minor engine families chosen by AWS; record the exact resolved versions after deployment. Record `git rev-parse HEAD`, `go version`, `terraform version`, the input variables, artifact SHA-256 values printed by the build script, and the saved plan in a secure run log. Review uncommitted source changes before building. The RDS CA is downloaded from Amazon's HTTPS truststore at build time; record its digest and review updates instead of assuming a later download is identical. Saved plans and Terraform state may contain sensitive infrastructure information and belong in protected storage, never Git. The repository ignores local artifacts, variables, plans and state. Restrict the local directory and any remote state backend; encryption and access control for a shared backend must be configured separately.

From the repository root, set a **restricted** IP CIDR and prepare a plan. These commands create no AWS resources. Data sources and planning do make read-only AWS API calls; verify the account and region first.

```sh
export AWS_PROFILE=your-profile
export AWS_REGION=us-east-1
export TF_VAR_allowed_cidr=203.0.113.10/32  # replace with your current public IP
aws sts get-caller-identity
go test ./...
terraform/scripts/build-artifacts.sh
terraform -chdir=terraform init
terraform -chdir=terraform fmt -check
terraform -chdir=terraform validate
terraform -chdir=terraform test  # mocked providers; no AWS resources
terraform -chdir=terraform plan -out=deployment.tfplan
terraform -chdir=terraform show deployment.tfplan
```

For optional Redis, set `export TF_VAR_enable_redis=true` before planning and keep it set for all subsequent operations, including teardown. Review the account, region, CIDR, image, all changes, credentials handling, and estimate. **Only after explicit approval**, the reviewed plan can be applied with `terraform -chdir=terraform apply deployment.tfplan`. The output is an HTTP ALB DNS URL; health can take several minutes while RDS, IAM, endpoints, and instances initialize. A plan is not proof that AWS quotas, service versions, bootstrap, or networking will work at apply time. A changed artifact causes instance replacement, which can interrupt service.

## Smoke test and screenshots

Only after an approved deployment is ready, from a host within the allowed CIDR:

```sh
BASE_URL=$(terraform -chdir=terraform output -raw base_url)
terraform/scripts/smoke.sh "$BASE_URL"
aws elbv2 describe-target-health --target-group-arn "$(terraform -chdir=terraform output -raw target_group_arn)" --region "$AWS_REGION"
```

The smoke test creates one link, reads it, and checks its redirect status without following the external destination. It leaves that record in the database. Confirm both targets report healthy. For a README screenshot, open the ALB URL from the allowed host, create a link with non-sensitive example data, capture the Link Lab page and optionally the ALB target-health screen, and crop out account IDs, public IP addresses, credentials, and unrelated browser content. Save the approved images under a clearly named documentation directory and link them in the main README. Do not fabricate screenshots or claim this deployment has been tested before actually running it.

## Optional k6 benchmark

**Do not run against AWS without explicit approval.** After approval and a successful smoke test, reuse its printed code. This sends point reads to the ALB only, does not follow redirects, and defaults to 5 offered requests/s for 30 seconds, capped at 25 requests/s and 120 seconds. No links are created by the workload.

```sh
ALLOW_AWS_LOAD_TEST=true BASE_URL="$BASE_URL" CODE=replace-with-smoke-code \
  RATE=5 SECONDS=30 k6 run terraform/aws-k6.js > aws-point-run.json
```

Record UTC time, instance types, AZs, dataset, cache state, approved rate, k6 version and output. Compare offered versus achieved request rate and examine errors, checks, dropped iterations and generator CPU before drawing conclusions. Latency statistics are per run, not pooled; short runs cannot establish a stable p99. Stop if errors, drops or saturation appear. This is an AWS measurement only if it is actually performed and documented.

## Illustrative cost estimate

As of November 2026, this is an **estimate, not a price quote** for `us-east-1`, on-demand Linux, 730 hours/month, no free-tier credits, light usage, and the default sizes. Check current rates in the [AWS Pricing Calculator](https://calculator.aws/) for your account and exact region before approval. Values rounded; services can change prices or availability.

| Component | Assumption | Approximate monthly cost |
| --- | --- | ---: |
| EC2 | 2 × t3.micro × $0.0104/hour | $15.18 |
| EC2 root EBS | 16 GB gp3 × $0.08/GB-month | $1.28 |
| RDS PostgreSQL | 1 × db.t3.micro, approximately $0.018/hour; confirm current rate | $13.14 |
| RDS storage | 20 GB gp3, approximately $0.115/GB-month; confirm current rate | $2.30 |
| ALB | $0.0225/hour, before LCU usage | $16.43 |
| ALB public IPv4 | 2 addresses × $0.005/hour | $7.30 |
| Secrets Manager interface endpoint | 1 AZ × $0.01/hour | $7.30 |
| Secrets Manager | 1 secret, approximately $0.40/month, before API calls | $0.40 |
| **Baseline subtotal** | Before variable charges | **$63.33/month** |
| Optional Redis | 1 × cache.t3.micro, approximately $0.017/hour; confirm current rate | **+$12.41/month** |

Eight hours prorated from these monthly assumptions is approximately $0.69 baseline, or $0.83 with Redis, before variable costs and billing minimums. RDS has a 10-minute minimum; ALB, EC2, IPv4 and endpoint charges accrue while provisioned. Add ALB LCUs, Secrets Manager API calls (two checks/minute), interface endpoint data processing, S3 requests/storage, data transfer including cross-AZ and internet egress, T3 CPU surplus credits, tax, and any retained resources or snapshots. A long benchmark or forgotten resources can cost much more. The prior project budget is $20 maximum, preferably under $5; **a full month exceeds it**. Use a short approved time window and verify billing after cleanup. AWS Budget notifications are alerts, not hard caps.

References: [EC2 T3 prices](https://aws.amazon.com/ec2/instance-types/t3/), [EBS pricing](https://aws.amazon.com/ebs/pricing/), [ALB pricing](https://aws.amazon.com/elasticloadbalancing/pricing/), [public IPv4 pricing](https://aws.amazon.com/vpc/pricing/), [PrivateLink pricing](https://aws.amazon.com/privatelink/pricing/), [RDS pricing](https://aws.amazon.com/rds/postgresql/pricing/), [ElastiCache pricing](https://aws.amazon.com/elasticache/pricing/), and [Secrets Manager pricing](https://aws.amazon.com/secrets-manager/pricing/). Verify the indicative RDS, storage and Redis rates in the calculator.

## Approved teardown and verification

The script operates only on the current Terraform state and configuration. It previews a saved destroy plan and requires two interactive confirmations. It does not remove manually created resources, retained snapshots, another state, or the local state/plan files. It does not force-empty a bucket containing objects outside Terraform; investigate such objects rather than deleting them blindly. Keep the same region, workspace and variable values used for deployment, including `TF_VAR_allowed_cidr` and Redis settings.

```sh
# Review the current workspace, account, configuration and state first:
aws sts get-caller-identity
terraform -chdir=terraform workspace show
terraform -chdir=terraform state list
# Only after explicit deletion approval:
terraform/scripts/teardown.sh --destroy
```

After completion, inspect Terraform state and the AWS console for the ALB, target group, instances, RDS and secret, optional Redis, S3 bucket and objects, VPC endpoints, volumes, network interfaces, security groups, subnets and VPC. Check for independently retained snapshots and logs before any separate deletion. Review AWS billing and cost reports later for delayed charges. Do not share or commit state, saved plans, credentials, or console screenshots containing sensitive details.
