#!/usr/bin/env bash
# scripts/build-agent.sh
# 在開發機（或 CI）編譯 Debian host agent 執行檔
# 產出：backup-agent（linux/amd64 靜態 binary，可直接複製到 Debian 上執行）
# 若本機未安裝 go，自動改用 Docker 編譯
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${ROOT}/backup-agent"

cd "$ROOT"

if command -v go &>/dev/null; then
  echo "[build] 使用本機 go 編譯 ..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o "$OUTPUT" ./cmd/agent
else
  echo "[build] 本機未找到 go，改用 Docker 編譯 ..."
  docker run --rm \
    -v "$ROOT":/src \
    -w /src \
    golang:1.23-bookworm \
    sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o /src/backup-agent ./cmd/agent"
fi

echo "[build] 完成：$OUTPUT"
echo ""
echo "部署步驟："
echo "  1. 複製到 Debian 主機：scp backup-agent user@host:/usr/local/bin/"
echo "  2. 在主機執行安裝腳本：sudo ./scripts/install-agent.sh"
