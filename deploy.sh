#!/bin/bash
# Runs on the server from the checkout: hard-sync to origin/main, rebuild, restart.
set -euo pipefail
cd "$(dirname "$0")"
git symbolic-ref -q HEAD >/dev/null || { echo "checkout is on a detached HEAD: git switch main" >&2; exit 1; }
git fetch origin
PREV=$(git rev-parse HEAD)
git reset --hard origin/main
export GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo "dev")
export BUILD_DATE=$(date -u +%Y-%m-%d)
docker compose up -d --build --remove-orphans

port="${HOST_PORT:-8150}"
healthy=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done

if [ "$healthy" -ne 1 ]; then
  echo "ROLLBACK: station-mcp unhealthy after deploy: rolling back to $PREV" >&2
  git reset --hard "$PREV"
  export GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo "dev")
  export BUILD_DATE=$(date -u +%Y-%m-%d)
  docker compose up -d --build --remove-orphans
  echo "ROLLBACK COMPLETE: restored $PREV" >&2
  exit 1
fi

echo "station-mcp deployed"
