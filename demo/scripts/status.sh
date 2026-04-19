#!/usr/bin/env bash
# Muestra un snapshot del estado de la demo: agente + endpoints vivos.
#
# Uso: ./demo/scripts/status.sh

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CTL="${ROOT}/keystonectl"

section() {
  printf "\n===== %s =====\n" "$1"
}

section "keystone: plan status"
"${CTL}" status || true

section "keystone: components"
"${CTL}" components || true

section "config-service :7001/config"
curl -s --max-time 2 http://localhost:7001/config || echo "(no responde)"
echo

section "data-producer :7002/events (últimos)"
curl -s --max-time 2 http://localhost:7002/events | head -c 600 || echo "(no responde)"
echo

section "data-consumer :7003/stats"
curl -s --max-time 2 http://localhost:7003/stats || echo "(no responde)"
echo
