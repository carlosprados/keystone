#!/usr/bin/env bash
# Arranca el agente Keystone en modo HTTP :8080, CWD = raíz del repo.
#
# Uso: ./demo/scripts/run-agent.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${ROOT}/keystone"

if [[ ! -x "${BIN}" ]]; then
  echo "[agent] ${BIN} no existe. Ejecuta primero 'task build' en la raíz." >&2
  exit 1
fi

cd "${ROOT}"
echo "[agent] arrancando keystone en :8080 (cwd=${ROOT})"
exec "${BIN}" --http :8080
