Trust bundle

- Place a PEM file with one or more trusted CA certificates at `configs/trust/ca.pem`.
- Set `KEYSTONE_TRUST_BUNDLE` to its path so Keystone verifies component signatures.
- Optionally set `KEYSTONE_LEAF_CERT` to a leaf certificate PEM if recipes do not provide `cert_uri`.

Signing with OpenSSL (example RSA):

1) Create a CA and sign a leaf cert (simplified, for development only):

```
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 -out ca.pem -subj "/CN=KeystoneDevCA"

openssl genrsa -out leaf.key 3072
openssl req -new -key leaf.key -out leaf.csr -subj "/CN=keystone-signer"
openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out leaf.pem -days 365 -sha256
```

2) Sign an artifact (detached sig over SHA-256):

```
openssl dgst -sha256 -sign leaf.key -out artifact.sig artifact.bin
```

3) Configure Keystone:

```
export KEYSTONE_TRUST_BUNDLE=/opt/keystone/etc/ca.pem
export KEYSTONE_LEAF_CERT=/opt/keystone/etc/leaf.pem
```

Or reference the signature and certificate from the recipe artifact:

```
[[artifacts]]
uri = "https://example.com/hello-linux-amd64"
sha256 = "..."
sig_uri = "https://example.com/hello-linux-amd64.sig"
cert_uri = "https://example.com/leaf.pem"
```

