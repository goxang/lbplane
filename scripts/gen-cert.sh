#!/usr/bin/env bash
set -euo pipefail

host=$1
out=$2
mkdir -p "$out"
openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
  -subj "/CN=$host" -addext "subjectAltName=DNS:$host" \
  -keyout "$out/${host%%.*}.key" -out "$out/${host%%.*}.crt" 2>/dev/null
cat "$out/${host%%.*}.crt" "$out/${host%%.*}.key" > "$out/${host%%.*}.pem"
