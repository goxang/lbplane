#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export NO_PROXY='*' no_proxy='*'

work=$(mktemp -d)
export SPECS_DIR="$work/services" CERTS_DIR="$work/certs"
cp -r deploy/services "$SPECS_DIR"
scripts/gen-cert.sh checkout.pay.test "$CERTS_DIR"

compose=(docker compose -f deploy/docker-compose.yml)

cleanup() {
  status=$?
  if [[ $status -ne 0 ]]; then
    "${compose[@]}" logs lbplane haproxy | tail -60
  fi
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1
  rm -rf "$work"
  exit $status
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

eventually() {
  local want=$1; shift
  for _ in $(seq 30); do
    if [[ "$("$@" 2>/dev/null)" == "$want" ]]; then return 0; fi
    sleep 1
  done
  fail "'$*' never returned '$want' (last: '$("$@" 2>&1 || true)')"
}

metric() {
  curl -s localhost:9100/metrics | awk -v name="$1" '$1 == name { print $2 }'
}

http_get() { curl -s -H "Host: $1" localhost:8080; }

"${compose[@]}" up -d --build --quiet-pull

echo "routes by host"
eventually "served by a" http_get checkout.pay.test
eventually "served by b" http_get refunds.pay.test
[[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: unknown.pay.test' localhost:8080)" == 404 ]] || fail "unknown host is not 404"

echo "terminates tls"
eventually "served by a" curl -sk --resolve checkout.pay.test:8443:127.0.0.1 https://checkout.pay.test:8443

echo "publishes dns"
eventually "172.28.0.10" dig +short @127.0.0.1 -p 1053 refunds.pay.test

echo "moves a backend without reload"
reloads=$(metric lbplane_haproxy_reloads_total)
sed -i 's/172.28.0.21/172.28.0.22/' "$SPECS_DIR/checkout.yaml"
eventually "served by b" http_get checkout.pay.test
[[ "$(metric lbplane_haproxy_reloads_total)" == "$reloads" ]] || fail "backend move triggered a reload"

echo "keeps serving through a broken spec"
sed -i 's/172.28.0.22:5678/refunds.internal:5678/' "$SPECS_DIR/refunds.yaml"
spec_failures_seen() { [[ -n "$(metric 'lbplane_reconcile_failures_total{stage="spec"}')" ]] && echo yes; }
eventually "yes" spec_failures_seen
[[ "$(http_get refunds.pay.test)" == "served by b" ]] || fail "refunds stopped serving"

echo "e2e passed"
