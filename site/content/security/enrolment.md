+++
title = "Device enrolment"
weight = 45
description = "Give each device its own MQTT identity, and keep it renewed."
+++

`keystone enrol` gives a device its own transport identity. The device generates a
key that never leaves it, gets a certificate for that key from an **enrolment
server**, and receives the CA to trust the broker with. From then on the agent
renews the certificate by itself.

This is about the connection to the broker. It has nothing to do with what code
the device runs: that is still decided by [signatures](../signing/) and the trust
bundle, and the two are kept apart on purpose.

## 1. The bootstrap file

An operator delivers a small JSON file to the device, out of band, readable only
by the user the agent runs as (`keystone` in the shipped units, mode `0600`):

```json
{
  "version": 1,
  "enrolUrl": "https://enrol.example:8443",
  "token": "kst1_<32 hex>_<43 base64url>",
  "tenant": "acme",
  "device": "pi-1",
  "caPin": "sha256:<64 hex>",
  "expiresAt": "2026-10-01T00:00:00Z"
}
```

- **`token`** is single-use and expires at `expiresAt`.
- **`caPin`** is the SHA-256 of the DER of the **CA** that the server's TLS chain
  ends at, never of the server's own certificate. The device trusts the server
  through that CA and nothing else: not the system roots, and not a public
  certificate for the same name.
- **`tenant`** and **`device`** become the certificate's name,
  `CN=<tenant>/<device>`, and the MQTT topic path `keystone/<tenant>/<device>/…`.
  They follow the [MQTT naming rules](../../control-planes/mqtt/#tenant-and-device-id):
  the tenant is a lowercase DNS label, the device ID 1 to 128 of `A-Z a-z 0-9 . _ -`.

A file with an unknown `version`, or any field the agent does not know, is refused.

## 2. Enrol

Run it as the user the agent runs as, so the agent can read the key and write
its renewals:

```bash
cd /var/lib/keystone
sudo -u keystone keystone enrol --bootstrap /var/lib/keystone/bootstrap.json --dir /var/lib/keystone/enrol
```

The `cd` lets it use the agent's clock mark in `runtime/state`.

What it does:

1. Checks the bootstrap, and that the token has not expired. Time comes from the
   agent's clock, so a device with no RTC is not fooled by a clock at 1970.
2. Generates a P-256 key and a certificate request for `CN=<tenant>/<device>`.
3. Sends `POST {enrolUrl}/enrol/v1` over TLS verified against the pinned CA.
4. Checks the answer before writing anything. The certificate must be for the key
   it just generated, under the name it asked for, and issued for clientAuth.
5. Writes the key (`0600`), the certificate chain and the broker CA.
6. Deletes the bootstrap file, since the token is spent.

A server that is rate limiting (`429`) is waited out, `Retry-After` plus jitter,
and asked again. A refused token (`403`: unknown, used or expired) writes nothing
and keeps the bootstrap file.

The directory already holding an identity is refused: it renews itself. To enrol a
device again (see [When renewal stops](#when-renewal-stops)), pass `--replace` with
a new bootstrap file.

`keystone enrol --help` lists the options.

## 3. Run the agent with it

```bash
KEYSTONE_ENROL_DIR=/var/lib/keystone/enrol
KEYSTONE_MQTT_BROKER=ssl://broker.example:8883
```

With `--enrol-dir` (`KEYSTONE_ENROL_DIR`) the agent takes from the enrolment:

- the MQTT client certificate and key, and the broker CA;
- the tenant (`--mqtt-tenant`) and the device ID (`--mqtt-device-id`).

Setting any of those explicitly to something else **refuses the start**. A device
ID that differs from the certificate's name would put the device under topics its
certificate does not cover, and a broker ACL drops those publishes without a word.

The broker URL is not part of the enrolment; set it as before.

## What is written

```text
/var/lib/keystone/enrol/
├── current -> gen-1790535000000000000
├── gen-1790535000000000000/
│   ├── device.key        0600
│   ├── device.crt        leaf + issuing CA
│   ├── broker-ca.pem
│   └── enrolment.json    server, pin, tenant, device, notAfter, renewAfter
└── gen-1790000000000000000/   the previous one, kept until the next renewal
```

Each enrolment or renewal writes a whole new generation and then switches
`current` in one rename. Key, certificate and broker CA therefore always change
together: nothing ever reads a new key with an old certificate, and an
interrupted write leaves a directory nobody points at.

The agent reads its files through `current`, so it sees a renewal as soon as the
link moves.

## Renewal

The agent renews at `renewAfter`, two thirds of the certificate's life, judged by
its [clock](../signing/#devices-whose-clock-cannot-be-trusted), which never goes backwards.

- It generates a **new** key, and sends `POST {enrolUrl}/enrol/v1/renew`
  authenticated by the current certificate over mTLS.
- Only after a `200` does the new key replace the old one. Until then, and after
  any failure, the device keeps using the old key and certificate: there are never
  two keys in play.
- On success it reconnects to the broker with the new certificate, without a
  restart. It does not wait for the next disconnect, because the server has
  already superseded the old certificate.
- Every connection attempt re-reads the certificate files, so an automatic
  reconnect uses them too.

| Answer | What the agent does |
|---|---|
| `200` | Switches to the new identity and reconnects |
| `429` | Waits `Retry-After` plus jitter, and asks again |
| Network error, `5xx` | Retries with backoff, 1 min doubling to 1 h |
| `400`, `401` | Logs it, keeps the right to renew, tries again in an hour |
| `403` | **Stops renewing.** See below |

### When renewal stops

Two situations need an operator, and the log says so as an `ERROR`:

- **`403 renewal refused`**: the device's certificate was superseded, revoked, or
  is unknown to the server. Retrying would not change that.
- **The certificate expired**: an expired certificate cannot authenticate a
  renewal.

The fix for both is a new bootstrap file and `keystone enrol --replace`. After a
`403` the current certificate still works until it expires; the log gives the
date.

## What the enrolment does not touch

The broker CA goes only to the MQTT connection. It is never added to
`KEYSTONE_TRUST_BUNDLE`, and the agent refuses to start if the two share a key
(see [Signing](../signing/#the-trust-bundle-is-for-code-only)). `keystone enrol`
runs the same check and warns at once if the result would be refused.

## Not covered

- **The broker's certificate** is checked against the system clock, not the
  agent's. A device with no RTC should get its time before connecting.
- **Revocation** is the server's business. The agent learns of it only as a
  `403` at renewal time; nothing on the device checks a revocation list.
