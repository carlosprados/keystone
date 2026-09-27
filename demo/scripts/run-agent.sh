#!/usr/bin/env bash
# Arranca el agente Keystone en HTTP 127.0.0.1:8080, CWD = raíz del repo,
# confiando en la CA de demo que crea build.sh.
#
# Uso: ./demo/scripts/run-agent.sh [flags extra del agente, p. ej. --mqtt-broker …]

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${ROOT}/keystone"

if [[ ! -x "${BIN}" ]]; then
  echo "[agent] ${BIN} no existe. Ejecuta primero 'task build' en la raíz." >&2
  exit 1
fi

TRUST="${ROOT}/demo/trust"
if [[ ! -f "${TRUST}/ca.pem" ]]; then
  echo "[agent] ${TRUST}/ca.pem no existe. Ejecuta primero ./demo/scripts/build.sh, que firma la demo." >&2
  exit 1
fi

cd "${ROOT}"
# The agent refuses unsigned recipes and artifacts, and refuses to serve its API
# off loopback without a token. The demo keeps both defaults: it trusts the demo
# CA and listens on 127.0.0.1.
export KEYSTONE_TRUST_BUNDLE="${TRUST}/ca.pem"
export KEYSTONE_LEAF_CERT="${TRUST}/leaf.pem"
echo "[agent] arrancando keystone en 127.0.0.1:8080 (cwd=${ROOT}, confía en ${TRUST}/ca.pem)"
exec "${BIN}" --http 127.0.0.1:8080 "$@"
