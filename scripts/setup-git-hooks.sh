#!/usr/bin/env sh
set -e
git config core.hooksPath .githooks
echo "✓ Git hooks enabled (core.hooksPath=.githooks)"

