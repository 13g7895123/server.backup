#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

echo "[deploy-agent] 編譯 backup-agent ..."
bash scripts/build-agent.sh

echo "[deploy-agent] 停止 backup-agent 服務..."
sudo systemctl stop backup-agent

echo "[deploy-agent] 複製 binary..."
sudo cp backup-agent /usr/local/bin/backup-agent

echo "[deploy-agent] 啟動 backup-agent 服務..."
sudo systemctl start backup-agent

echo "[deploy-agent] 完成。"
sudo systemctl status backup-agent --no-pager
