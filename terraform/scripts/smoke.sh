#!/usr/bin/env bash
set -euo pipefail
base=${1:?Usage: smoke.sh http://ALB-DNS}
case "$base" in http://*) ;; *) echo 'Expected the explicit HTTP ALB URL.' >&2; exit 2;; esac
base=${base%/}
for path in /health/live /health/ready /health/serving; do
  printf '%s ' "$path"
  curl --fail --silent --show-error --max-time 10 "$base$path"
  printf '\n'
done
response=$(curl --fail --silent --show-error --max-time 10 -H 'Content-Type: application/json' -d '{"userId":"aws-smoke","destinationUrl":"https://example.com/"}' "$base/links")
code=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["code"])')
printf 'Created code: %s\n' "$code"
curl --fail --silent --show-error --max-time 10 "$base/links/$code"
printf '\nRedirect status (destination is never contacted): '
status=$(curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}' "$base/u/$code")
printf '%s\n' "$status"
[[ "$status" == 302 ]] || { echo 'Unexpected redirect status' >&2; exit 1; }
