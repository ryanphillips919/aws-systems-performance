#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ ${1:-} != '--destroy' || $# -ne 1 ]]; then
  echo 'No deletion performed. Review terraform plan -destroy, then explicitly run:'
  echo '  terraform/scripts/teardown.sh --destroy'
  exit 2
fi
printf '%s\n' 'This destroys resources tracked by this Terraform state, including the database and its data. It does not remove unrelated resources.'
read -r -p 'Type DESTROY to continue: ' answer
[[ "$answer" == DESTROY ]] || { echo 'Cancelled.'; exit 1; }
terraform plan -destroy -out=teardown.tfplan
terraform show teardown.tfplan
read -r -p 'Type DESTROY again to execute this exact plan: ' answer
[[ "$answer" == DESTROY ]] || { echo 'Cancelled; the saved plan remains local.'; exit 1; }
terraform apply teardown.tfplan
rm -f teardown.tfplan
printf '%s\n' 'Verify the state is empty and check the AWS console for any retained snapshots, logs, buckets, and delayed charges.'
terraform state list
