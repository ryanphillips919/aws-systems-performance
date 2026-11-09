#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p "$root/terraform/artifacts"
cd "$root"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o terraform/artifacts/api ./cmd/api
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem -o terraform/artifacts/global-bundle.pem
sha256sum terraform/artifacts/api terraform/artifacts/global-bundle.pem
