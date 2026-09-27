#!/usr/bin/env bash
# Compila los 6 binarios de demo (v1 y v2), calcula SHA-256,
# renderiza las plantillas de recipes con los hashes reales y firma
# binarios y recipes con una CA de demo.
#
# Uso: ./demo/scripts/build.sh
# CWD esperado: raíz del repo keystone.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEMO="${ROOT}/demo"
ARTIFACTS="${DEMO}/artifacts"
SRC="${DEMO}/services"

echo "[build] raíz del repo: ${ROOT}"
mkdir -p "${ARTIFACTS}"

build_one() {
  local bin_name="$1" pkg_path="$2"
  echo "[build] -> ${bin_name}"
  (cd "${SRC}" && go build -trimpath -ldflags="-s -w" -o "${ARTIFACTS}/${bin_name}" "${pkg_path}")
}

build_one "config-service-v1" "./config-service/v1"
build_one "config-service-v2" "./config-service/v2"
build_one "data-producer-v1"  "./data-producer/v1"
build_one "data-producer-v2"  "./data-producer/v2"
build_one "data-consumer-v1"  "./data-consumer/v1"
build_one "data-consumer-v2"  "./data-consumer/v2"

sha_of() {
  sha256sum "$1" | awk '{print $1}'
}

render() {
  local tmpl="$1" out="$2" bin="$3"
  local hash
  hash="$(sha_of "${ARTIFACTS}/${bin}")"
  sed "s|{{SHA256}}|${hash}|g" "${tmpl}" > "${out}"
  echo "[build] rendered ${out#${ROOT}/} (sha256=${hash:0:12}...)"
}

render "${DEMO}/recipes/v1/com.demo.config.recipe.toml.tmpl"   "${DEMO}/recipes/v1/com.demo.config.recipe.toml"   "config-service-v1"
render "${DEMO}/recipes/v1/com.demo.producer.recipe.toml.tmpl" "${DEMO}/recipes/v1/com.demo.producer.recipe.toml" "data-producer-v1"
render "${DEMO}/recipes/v1/com.demo.consumer.recipe.toml.tmpl" "${DEMO}/recipes/v1/com.demo.consumer.recipe.toml" "data-consumer-v1"

render "${DEMO}/recipes/v2/com.demo.config.recipe.toml.tmpl"   "${DEMO}/recipes/v2/com.demo.config.recipe.toml"   "config-service-v2"
render "${DEMO}/recipes/v2/com.demo.producer.recipe.toml.tmpl" "${DEMO}/recipes/v2/com.demo.producer.recipe.toml" "data-producer-v2"
render "${DEMO}/recipes/v2/com.demo.consumer.recipe.toml.tmpl" "${DEMO}/recipes/v2/com.demo.consumer.recipe.toml" "data-consumer-v2"

# The agent verifies everything it installs, and the demo shows that rather
# than switching it off: a throwaway CA in demo/trust/ signs every binary (its
# .sig is served next to it, at the recipe's sig_uri) and every recipe (the
# agent reads <recipe>.sig beside the file).
echo "[build] firmando binarios y recipes con la CA de demo (demo/trust/)"
KEYSTONE_TRUST_DIR="${DEMO}/trust" "${ROOT}/scripts/dev-sign.sh" \
  "${ARTIFACTS}"/config-service-v1 "${ARTIFACTS}"/config-service-v2 \
  "${ARTIFACTS}"/data-producer-v1 "${ARTIFACTS}"/data-producer-v2 \
  "${ARTIFACTS}"/data-consumer-v1 "${ARTIFACTS}"/data-consumer-v2 \
  "${DEMO}"/recipes/v1/*.recipe.toml "${DEMO}"/recipes/v2/*.recipe.toml

echo "[build] OK"
ls -lh "${ARTIFACTS}"
