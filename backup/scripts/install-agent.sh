#!/usr/bin/env bash
# scripts/install-agent.sh
# 在 Debian 主機上安裝 backup-agent 為 systemd 服務
# 需要以 root 執行：sudo ./install-agent.sh
set -euo pipefail

BINARY_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/backup-agent"
BINARY_DST="/usr/local/bin/backup-agent"
ENV_DIR="/etc/backup-agent"
ENV_FILE="${ENV_DIR}/env"
SERVICE_SRC="$(dirname "${BASH_SOURCE[0]}")/backup-agent.service"
SERVICE_DST="/etc/systemd/system/backup-agent.service"

# ── 確認 binary 已編譯 ────────────────────────────────────────────
if [[ ! -f "$BINARY_SRC" ]]; then
  echo "[error] 找不到 backup-agent binary，請先執行 scripts/build-agent.sh"
  exit 1
fi

# ── 安裝 binary ───────────────────────────────────────────────────
echo "[install] 複製 binary → $BINARY_DST"
cp "$BINARY_SRC" "$BINARY_DST"
chmod 755 "$BINARY_DST"

# ── 建立設定目錄與 env 檔 ─────────────────────────────────────────
mkdir -p "$ENV_DIR"
if [[ ! -f "$ENV_FILE" ]]; then
  # 嘗試從 .env 讀取 PG_PASSWORD
  PROJ_ENV="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.env"
  DASHBOARD_URL=""
  AGENT_TOKEN=""
  if [[ -f "$PROJ_ENV" ]]; then
    DASHBOARD_URL=$(grep -E '^DASHBOARD_URL=' "$PROJ_ENV" | cut -d= -f2 | tr -d '\r\n' || true)
    AGENT_TOKEN=$(grep -E '^AGENT_TOKEN='    "$PROJ_ENV" | cut -d= -f2 | tr -d '\r\n' || true)
  fi
  # 若 .env 未設，使用預設值
  DASHBOARD_URL="${DASHBOARD_URL:-http://127.0.0.1:8105}"
  cat > "$ENV_FILE" <<EOF
# backup-agent 環境設定
# Dashboard API 位址（agent 透過 HTTP 與 dashboard 溝通，不直連 DB）
DASHBOARD_URL=${DASHBOARD_URL}

# Agent Token（選填，與 dashboard AGENT_TOKEN 對應）
AGENT_TOKEN=${AGENT_TOKEN}

# HOST_PREFIX 留空 = agent 直接讀取 host 路徑（不走 Docker volume 前綴）
HOST_PREFIX=

# NAS 掛載點（agent 寫入備份的目標）
NAS_BASE=/mnt/nas/backups

# Slack 失敗通知（選填）
SLACK_WEBHOOK_URL=
EOF
  echo "[install] 建立設定檔：$ENV_FILE（請確認 DASHBOARD_URL 正確）"
else
  echo "[install] 設定檔已存在，跳過：$ENV_FILE"
fi

# ── 安裝 systemd service ──────────────────────────────────────────
echo "[install] 安裝 systemd service → $SERVICE_DST"
cp "$SERVICE_SRC" "$SERVICE_DST"
systemctl daemon-reload
systemctl enable backup-agent
systemctl restart backup-agent

echo ""
echo "[install] 完成！"
echo "  狀態：systemctl status backup-agent"
echo "  日誌：journalctl -fu backup-agent"
echo "  設定：$ENV_FILE"
