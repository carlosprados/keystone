#!/usr/bin/env bash
#
# dev-sign.sh — development-only signing helper for Keystone.
#
# Generates a throwaway CA + leaf signer under configs/trust/ (once), then
# produces a detached SHA-256 signature for each file passed as an argument.
# Keystone requires recipes loaded from a file (and any declared artifacts) to
# be signed; this script lets you exercise the SECURE path locally without
# --insecure-skip-verify.
#
# WARNING: the keys produced here are NOT secret and NOT for production. They
# exist only to demo signature verification on a dev machine.
#
# Usage:
#   scripts/dev-sign.sh configs/examples/com.keystone.server.recipe.toml [more files...]
#
# Then run the agent against the generated trust material:
#   export KEYSTONE_TRUST_BUNDLE=configs/trust/ca.pem
#   export KEYSTONE_LEAF_CERT=configs/trust/leaf.pem
#   ./keystone --http 127.0.0.1:8080
#
set -euo pipefail

TRUST_DIR="${KEYSTONE_TRUST_DIR:-configs/trust}"
CA_KEY="$TRUST_DIR/ca.key"
CA_PEM="$TRUST_DIR/ca.pem"
LEAF_KEY="$TRUST_DIR/leaf.key"
LEAF_PEM="$TRUST_DIR/leaf.pem"

mkdir -p "$TRUST_DIR"

if [[ ! -f "$CA_PEM" || ! -f "$LEAF_PEM" ]]; then
  echo "[dev-sign] generating development CA + leaf signer in $TRUST_DIR"
  openssl genrsa -out "$CA_KEY" 4096 >/dev/null 2>&1
  openssl req -x509 -new -nodes -key "$CA_KEY" -sha256 -days 3650 \
    -out "$CA_PEM" -subj "/CN=KeystoneDevCA" >/dev/null 2>&1
  openssl genrsa -out "$LEAF_KEY" 3072 >/dev/null 2>&1
  openssl req -new -key "$LEAF_KEY" -out "$TRUST_DIR/leaf.csr" \
    -subj "/CN=keystone-dev-signer" >/dev/null 2>&1
  openssl x509 -req -in "$TRUST_DIR/leaf.csr" -CA "$CA_PEM" -CAkey "$CA_KEY" \
    -CAcreateserial -out "$LEAF_PEM" -days 365 -sha256 >/dev/null 2>&1
  rm -f "$TRUST_DIR/leaf.csr"
fi

if [[ $# -eq 0 ]]; then
  echo "[dev-sign] CA/leaf ready. Pass files to sign, e.g.:"
  echo "  $0 configs/examples/com.keystone.server.recipe.toml"
  exit 0
fi

for f in "$@"; do
  if [[ ! -f "$f" ]]; then
    echo "[dev-sign] skip (not a file): $f" >&2
    continue
  fi
  openssl dgst -sha256 -sign "$LEAF_KEY" -out "$f.sig" "$f"
  # Self-check the signature verifies against the leaf public key.
  openssl dgst -sha256 -verify <(openssl x509 -in "$LEAF_PEM" -pubkey -noout) \
    -signature "$f.sig" "$f" >/dev/null
  echo "[dev-sign] signed: $f -> $f.sig"
done

echo
echo "[dev-sign] done. Export the trust material before running the agent:"
echo "  export KEYSTONE_TRUST_BUNDLE=$CA_PEM"
echo "  export KEYSTONE_LEAF_CERT=$LEAF_PEM"
