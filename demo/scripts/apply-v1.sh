#!/usr/bin/env bash
# Aplica el plan v1: despliega config, producer y consumer en versión 1.0.0.
#
# Uso: ./demo/scripts/apply-v1.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CTL="${ROOT}/keystonectl"
PLAN="demo/plans/plan-v1.toml"

if [[ ! -x "${CTL}" ]]; then
  echo "[apply] ${CTL} no existe. Ejecuta 'task build' en la raíz." >&2
  exit 1
fi

cd "${ROOT}"
echo "[apply] aplicando ${PLAN} vía HTTP"
"${CTL}" apply "${PLAN}"
