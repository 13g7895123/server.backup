#!/usr/bin/env bash
# scripts/build-agent.sh
# 在開發機（或 CI）編譯 Debian host agent 執行檔
# 產出：backup-agent（linux/amd64 靜態 binary，可直接複製到 Debian 上執行）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${ROOT}/backup-agent"

echo "[build] 編譯 host agent binary ..."
cd "$ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$OUTPUT" ./cmd/agent

echo "[build] 完成：$OUTPUT"
echo ""
echo "部署步驟："
echo "  1. 複製到 Debian 主機：scp backup-agent user@host:/usr/local/bin/"
echo "  2. 在主機執行安裝腳本：sudo ./scripts/install-agent.sh"
