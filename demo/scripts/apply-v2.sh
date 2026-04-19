#!/usr/bin/env bash
# Aplica el plan v2: actualiza los tres componentes a 2.0.0 de forma atómica.
#
# Uso: ./demo/scripts/apply-v2.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CTL="${ROOT}/keystonectl"
PLAN="demo/plans/plan-v2.toml"

if [[ ! -x "${CTL}" ]]; then
  echo "[apply] ${CTL} no existe. Ejecuta 'task build' en la raíz." >&2
  exit 1
fi

cd "${ROOT}"
echo "[apply] actualizando a v2 con ${PLAN}"
"${CTL}" apply "${PLAN}"
