#!/bin/bash
# diagnose-disk-usage.sh — 診斷 /api/disk-usage 404 問題

set -euo pipefail
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }

echo "=========================================="
echo " 診斷 /api/disk-usage 404"
echo "=========================================="

# ── 1. 確認 docker compose 服務狀態 ──────────────────────────────────────────
echo ""
echo "▶ [1] docker compose ps"
docker compose ps

# ── 2. 確認容器建立時間（太舊 = 未重建）────────────────────────────────────
echo ""
echo "▶ [2] 容器 Created 時間"
CONTAINER=$(docker compose ps -q dashboard 2>/dev/null | head -1)
if [[ -z "$CONTAINER" ]]; then
  fail "找不到 dashboard 容器，請確認 docker compose ps"
  exit 1
fi
CREATED=$(docker inspect --format '{{.Created}}' "$CONTAINER")
ok "dashboard 容器 ID: $CONTAINER"
echo "   Created: $CREATED"

# ── 3. 確認 image 建立時間 ────────────────────────────────────────────────────
echo ""
echo "▶ [3] Image 建立時間"
IMAGE=$(docker inspect --format '{{.Image}}' "$CONTAINER")
IMG_CREATED=$(docker inspect --format '{{.Created}}' "$IMAGE" 2>/dev/null || echo "unknown")
echo "   Image: $IMAGE"
echo "   Image Created: $IMG_CREATED"

# ── 4. 在容器內直接呼叫 API（繞過 reverse proxy）──────────────────────────
echo ""
echo "▶ [4] 容器內部直接呼叫 /api/disk-usage（繞過 nginx/proxy）"
INTERNAL=$(docker exec "$CONTAINER" wget -qO- http://localhost:8080/api/disk-usage 2>&1 || true)
if echo "$INTERNAL" | grep -q "404"; then
  fail "容器內部也 404 → 路由未正確註冊（image 是舊的）"
  echo "   回應: $INTERNAL"
elif echo "$INTERNAL" | grep -q "partitions\|collected_at"; then
  ok "容器內部回應正常 → 問題在 reverse proxy"
  echo "   回應片段: ${INTERNAL:0:200}"
else
  warn "未知回應: $INTERNAL"
fi

# ── 5. 確認 binary 是否包含 disk-usage 字串 ─────────────────────────────────
echo ""
echo "▶ [5] 檢查 binary 是否含有 disk-usage 路由字串"
BINARY_CHECK=$(docker exec "$CONTAINER" sh -c 'strings /app/dashboard 2>/dev/null | grep "disk-usage" | head -5' 2>&1 || true)
if [[ -z "$BINARY_CHECK" ]]; then
  fail "binary 內找不到 'disk-usage' → binary 是舊版，需重建 image"
else
  ok "binary 內找到 disk-usage 相關字串:"
  echo "$BINARY_CHECK" | sed 's/^/   /'
fi

# ── 6. 確認環境變數 AGENT_URL ──────────────────────────────────────────────
echo ""
echo "▶ [6] 環境變數 AGENT_URL"
AGENT_URL=$(docker exec "$CONTAINER" sh -c 'echo $AGENT_URL' 2>/dev/null || true)
if [[ -z "$AGENT_URL" ]]; then
  warn "AGENT_URL 未設定 → disk-usage 將直接在容器內執行 df"
else
  ok "AGENT_URL=$AGENT_URL"
  echo "   → disk-usage 會 proxy 到 agent"
  echo "   測試 agent 連線..."
  AGENT_RESP=$(docker exec "$CONTAINER" wget -qO- "${AGENT_URL}/disk-usage" 2>&1 || true)
  if echo "$AGENT_RESP" | grep -q "partitions\|collected_at"; then
    ok "Agent /disk-usage 回應正常"
  else
    fail "Agent /disk-usage 回應異常: $AGENT_RESP"
  fi
fi

# ── 7. 最近容器 log ───────────────────────────────────────────────────────────
echo ""
echo "▶ [7] 最近 20 行容器 log"
docker logs --tail 20 "$CONTAINER" 2>&1

# ── 8. 結論 ──────────────────────────────────────────────────────────────────
echo ""
echo "=========================================="
echo " 結論"
echo "=========================================="
if [[ -z "$BINARY_CHECK" ]]; then
  fail "需要重建 image："
  echo ""
  echo "   docker compose build dashboard && docker compose up -d"
else
  ok "image 是新的，請檢查上方的 proxy/agent 錯誤訊息"
fi
