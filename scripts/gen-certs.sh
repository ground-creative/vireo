#!/usr/bin/env bash
# gen-certs.sh writes a self-signed certificate for local use.
#
#   ./scripts/gen-certs.sh              # certs/cert.pem + certs/key.pem
#   ./scripts/gen-certs.sh /tmp/certs   # another directory
#
# The certificate is valid for localhost and 127.0.0.1 for ten years. It is
# for development only: production needs a real certificate for your hostname
# (certbot, ACME, or your CA).
set -euo pipefail

dir="${1:-certs}"
mkdir -p "$dir"

openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "$dir/key.pem" \
  -out "$dir/cert.pem" \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:vireo.local,IP:127.0.0.1,IP:::1"

chmod 600 "$dir/key.pem"
chmod 644 "$dir/cert.pem"

echo "wrote $dir/cert.pem and $dir/key.pem"
openssl x509 -in "$dir/cert.pem" -noout -subject -dates
