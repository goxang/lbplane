# lbplane

A small control plane for HAProxy. Teams describe their service in a YAML file, lbplane turns all of them
into one HAProxy config, applies it, and publishes the hostnames to DNS. No tickets, no hand-edited
`haproxy.cfg`.

```yaml
name: checkout
owner: payments-team
hostnames: [checkout.pay.test]
health_check: /healthz
tls_cert: /certs/checkout.pem
backends:
  - address: 10.0.0.21:8080
  - address: 10.0.0.22:8080
    weight: 50
```

## How it works

Every few seconds (or on `SIGHUP`) lbplane reads the spec directory and compares the result with what it
applied last.

- **Backend changes don't reload HAProxy.** Each backend gets spare server slots rendered as `disabled`.
  Adding, removing, or re-weighting a backend is sent through the runtime API (`set server ... addr/weight/state`)
  and backends keep their slot, so running connections are untouched.
- **Everything else is a reload.** New service, hostname, TLS cert, or more backends than slots: the new config
  is written to a candidate file, checked with `haproxy -c`, moved into place, then reloaded through the master
  socket.
- **DNS.** After a successful apply, a hosts file is written for CoreDNS, pointing every hostname at the VIP.

## Failure behavior

| What breaks | What happens |
|---|---|
| One spec file is invalid, or two teams claim the same hostname | Nothing is applied, last good config keeps serving, `lbplane_reconcile_failures_total{stage="spec"}` goes up |
| Rendered config fails `haproxy -c` | Current config stays on disk and in HAProxy |
| A runtime command fails halfway | lbplane falls back to a full reload so HAProxy matches the file again |
| lbplane is down | HAProxy keeps serving the last config, it is only the control plane |

## Run it

```bash
make demo                 # HAProxy, CoreDNS, two echo backends and lbplane in Docker
curl -H 'Host: refunds.pay.test' localhost:8080
curl -k --resolve checkout.pay.test:8443:127.0.0.1 https://checkout.pay.test:8443
dig @127.0.0.1 -p 1053 +short checkout.pay.test
curl -s localhost:9100/metrics | grep lbplane_
make down
```

Edit a file in `deploy/services/` while it runs and watch the change land.

Teams can check their spec in their own CI before merging:

```bash
lbplane validate --specs services --haproxy-bin haproxy
```

## Development

```bash
make test    # unit tests, the haproxy -c test runs if haproxy is installed
make lint
make e2e     # the full stack in Docker Compose
```

## Limits

- Backend addresses must be IPs. The runtime API can't change a server to a hostname.
- Specs are polled, not watched. Fine for a directory synced from git, not for thousands of changes a second.
- One HAProxy instance. Several nodes would need the same config pushed to each, which is the next step.
