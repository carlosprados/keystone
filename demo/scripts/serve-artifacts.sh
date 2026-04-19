#!/usr/bin/env bash
# Arranca keystoneserver sirviendo los binarios de demo en :9000.
#
# Uso: ./demo/scripts/serve-artifacts.sh
# Requiere: haber ejecutado antes `task build` en la raíz del repo
# (o directamente `go build ./cmd/keystoneserver`).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ARTIFACTS="${ROOT}/demo/artifacts"
BIN="${ROOT}/keystoneserver"

if [[ ! -x "${BIN}" ]]; then
  echo "[serve] ${BIN} no existe. Ejecuta primero 'task build' en la raíz." >&2
  exit 1
fi

if [[ ! -d "${ARTIFACTS}" || -z "$(ls -A "${ARTIFACTS}" 2>/dev/null)" ]]; then
  echo "[serve] ${ARTIFACTS} vacío. Ejecuta primero ./demo/scripts/build.sh" >&2
  exit 1
fi

echo "[serve] sirviendo ${ARTIFACTS} en http://localhost:9000"
exec "${BIN}" --addr ":9000" --root "${ARTIFACTS}"
