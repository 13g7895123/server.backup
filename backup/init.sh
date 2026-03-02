#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"
SECRET_FILE="${SCRIPT_DIR}/secrets/pg_password.txt"

# ── 產生 8 碼隨機密碼（英數混合）─────────────────────────────────────────────
NEW_PASS=$(openssl rand -base64 12 | tr -dc 'A-Za-z0-9' | head -c 8 || true)
# 若 openssl 不可用，fallback 到 /dev/urandom
if [[ -z "$NEW_PASS" ]]; then
  NEW_PASS=$(cat /dev/urandom | tr -dc 'A-Za-z0-9' | dd bs=1 count=8 2>/dev/null)
fi

# ── 若 .env 不存在則從範本建立 ────────────────────────────────────────────────
if [[ ! -f "$ENV_FILE" ]]; then
  cp "${SCRIPT_DIR}/.env.example" "$ENV_FILE"
  echo "[init] 已從 .env.example 建立 .env"
fi

# ── 更新 .env 中的 PG_PASSWORD ────────────────────────────────────────────────
sed -i "s/^PG_PASSWORD=.*/PG_PASSWORD=${NEW_PASS}/" "$ENV_FILE"
echo "[init] .env PG_PASSWORD 已更新"

# ── 更新 secrets/pg_password.txt ─────────────────────────────────────────────
mkdir -p "$(dirname "$SECRET_FILE")"
echo -n "$NEW_PASS" > "$SECRET_FILE"
echo "[init] secrets/pg_password.txt 已更新"

echo ""
echo "======================================"
echo "  新密碼: ${NEW_PASS}"
echo "  請妥善保存，此訊息不會再次顯示"
echo "======================================"
