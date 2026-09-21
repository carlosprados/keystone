+++
title = "Verifying a release"
weight = 43
description = "What ships alongside the binaries, and how to check it before you install."
+++

Keystone asks you to sign everything it installs. It would be hard to defend if
it did not sign itself.

From **v0.9.0** every release carries, beside the archives:

| File | What it is |
|---|---|
| `checksums.txt` | SHA-256 of every other file in the release |
| `checksums.txt.sig` | Signature over that file |
| `checksums.txt.pem` | The short-lived certificate that made the signature |
| `keystone_<version>_linux_<arch>.tar.gz.sbom.json` | SPDX 2.3 inventory of the modules inside that archive |

## Why the signature is not the checksum

`checksums.txt` proves **integrity**: the bytes you downloaded are the bytes
that were published. It proves nothing about *who* published them — whoever can
replace an archive can replace the checksum file sitting next to it.

The signature proves **provenance**: these bytes were produced by this
repository's release workflow, running on a tag. That is the property that
matters for a deployment agent, because whoever controls the agent controls
everything it installs afterwards.

There is no private key anywhere in this. Signing is keyless: the workflow
exchanges a short-lived OIDC token for a certificate that names it, signs, and
the certificate expires. The signature is recorded in Sigstore's public
transparency log, which is what makes a forgery **detectable** rather than
merely hard.

## Checking it

Needs [cosign](https://docs.sigstore.dev/cosign/installation/).

```bash
VERSION=v0.9.0
BASE=https://github.com/carlosprados/keystone/releases/download/$VERSION

curl -fsSLO $BASE/checksums.txt
curl -fsSLO $BASE/checksums.txt.sig
curl -fsSLO $BASE/checksums.txt.pem
curl -fsSLO $BASE/keystone_${VERSION#v}_linux_amd64.tar.gz

cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/carlosprados/keystone/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

sha256sum --check --ignore-missing checksums.txt
```

The first command answers *"did this repository's release workflow produce this
list?"*; the second answers *"is my download on that list?"*. Both, in that
order, or neither is worth much.

**Pin the identity.** Dropping `--certificate-identity-regexp` makes the
verification accept a signature from any workflow in any repository, which
passes just as happily against an attacker's fork. Verifying against an
unpinned identity is theatre.

## The SBOM

One SPDX 2.3 document per archive, produced by
[syft](https://github.com/anchore/syft), listing every Go module compiled into
the binaries inside it.

```bash
# Which version of a dependency is in this build?
jq -r '.packages[] | "\(.name) \(.versionInfo)"' \
  keystone_0.9.0_linux_amd64.tar.gz.sbom.json | sort

# Does the CVE that just landed affect us?
grep -i golang.org/x/crypto keystone_0.9.0_linux_amd64.tar.gz.sbom.json
```

The SBOMs are listed in `checksums.txt`, so the signature covers them too.

A Go binary already embeds its own module list — `go version -m keystone` shows
it — but that is not a format a scanner, an auditor or a customer can consume,
and it requires having the binary to hand rather than the release page.

## What this does not prove

- **Not that the build is reproducible.** The signature says these bytes came
  from that workflow, not that you would get the same bytes by rebuilding the
  source yourself.
- **Not that the source is sound.** Provenance is not review. A signed release
  of a compromised commit verifies perfectly.
- **Nothing about releases before v0.9.0.** Earlier tags carry `checksums.txt`
  and nothing else; they cannot be verified retroactively, because the
  certificate has to be minted at signing time.
