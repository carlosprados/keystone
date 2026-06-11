# Trust bundle and signing

Keystone verifies signatures, by default and fail-closed, on:

- **Artifacts** declared in a recipe (`sig_uri` + `sha256`).
- **Recipes loaded from a file** (a sibling `<recipe>.sig`), before any
  lifecycle hook runs.

Both chain to the CA bundle in `KEYSTONE_TRUST_BUNDLE`. See
[docs/security.md](../../docs/security.md) for the full model.

## Quick start (development)

The helper script generates a throwaway dev CA and signs files for you:

```bash
# Generates configs/trust/{ca,leaf}.{key,pem} (gitignored) and a .sig per file.
scripts/dev-sign.sh configs/examples/com.keystone.server.recipe.toml

export KEYSTONE_TRUST_BUNDLE=configs/trust/ca.pem
export KEYSTONE_LEAF_CERT=configs/trust/leaf.pem
```

The generated keys are **not secret** and are gitignored. Never use them in
production.

## Manual signing with OpenSSL

### 1. Create a CA and a leaf signer (development only)

```bash
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 -out ca.pem -subj "/CN=KeystoneDevCA"

openssl genrsa -out leaf.key 3072
openssl req -new -key leaf.key -out leaf.csr -subj "/CN=keystone-signer"
openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out leaf.pem -days 365 -sha256
```

### 2. Sign a recipe (detached signature, verified before any hook runs)

```bash
openssl dgst -sha256 -sign leaf.key -out com.example.app.recipe.toml.sig com.example.app.recipe.toml
```

Place `com.example.app.recipe.toml.sig` next to the recipe; provide the cert as
`com.example.app.recipe.toml.crt` or via `KEYSTONE_LEAF_CERT`.

### 3. Sign an artifact (detached sig over SHA-256)

```bash
openssl dgst -sha256 -sign leaf.key -out artifact.sig artifact.bin
```

### 4. Configure Keystone

```bash
export KEYSTONE_TRUST_BUNDLE=/opt/keystone/etc/ca.pem
export KEYSTONE_LEAF_CERT=/opt/keystone/etc/leaf.pem
```

Or reference the signature and certificate from the recipe artifact:

```toml
[[artifacts]]
uri = "https://example.com/hello-linux-amd64"
sha256 = "..."
sig_uri = "https://example.com/hello-linux-amd64.sig"
cert_uri = "https://example.com/leaf.pem"
```

## Production notes

- Keep the CA and signing private keys **off the device**; sign in CI or on a
  signing host and ship only the public CA bundle + signatures.
- For local experiments with unsigned recipes/artifacts, run the agent with
  `--insecure-skip-verify` (or `KEYSTONE_INSECURE_SKIP_VERIFY=true`) instead of
  weakening the trust setup.
