#!/usr/bin/env bash
# Limpia el estado de demo: para el plan, borra runtime/ y artefactos generados.
#
# Uso: ./demo/scripts/clean.sh [--deep]
#   --deep  también borra los binarios renderizados y las recipes generadas.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CTL="${ROOT}/keystonectl"
DEEP=false
[[ "${1:-}" == "--deep" ]] && DEEP=true

echo "[clean] parando plan en el agente (si está activo)"
"${CTL}" stop-plan 2>/dev/null || true

echo "[clean] borrando runtime/"
rm -rf "${ROOT}/runtime"

if $DEEP; then
  echo "[clean] --deep: borrando artefactos demo y recipes renderizadas"
  rm -f "${ROOT}/demo/artifacts"/*
  rm -f "${ROOT}/demo/recipes/v1/"*.toml
  rm -f "${ROOT}/demo/recipes/v2/"*.toml
fi

echo "[clean] OK"
