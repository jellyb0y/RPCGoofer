#!/bin/bash
set -euo pipefail

IMAGE="jellyb0y/rpcgofer"
TAG="latest"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> Building $IMAGE:$TAG"
docker build -t "$IMAGE:$TAG" "$ROOT_DIR"

echo "==> Pushing $IMAGE:$TAG"
docker push "$IMAGE:$TAG"

if [[ "$TAG" != "latest" ]]; then
  docker tag "$IMAGE:$TAG" "$IMAGE:latest"
  docker push "$IMAGE:latest"
fi

echo "==> Done"
