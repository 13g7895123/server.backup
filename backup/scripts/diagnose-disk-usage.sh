#!/bin/bash
# diagnose-disk-usage.sh — 診斷 /api/disk-usage 404 問題

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

# ── 2. 確認容器建立時間 ────────────────────────────────────────────────────
echo ""
echo "▶ [2] 容器 & Image 時間"
CONTAINER=$(docker compose ps -q dashboard 2>/dev/null | head -1)
if [[ -z "$CONTAINER" ]]; then
  fail "找不到 dashboard 容器"
  exit 1
fi
CREATED=$(docker inspect --format '{{.Created}}' "$CONTAINER")
IMAGE=$(docker inspect --format '{{.Image}}' "$CONTAINER")
IMG_CREATED=$(docker inspect --format '{{.Created}}' "$IMAGE" 2>/dev/null || echo "unknown")
ok "Container Created : $CREATED"
ok "Image    Created  : $IMG_CREATED"

# ── 3. 取得 dashboard 對外 port ──────────────────────────────────────────────
echo ""
echo "▶ [3] 取得 dashboard 對外 port"
HOST_PORT=$(docker inspect --format '{{range $p,$b := .NetworkSettings.Ports}}{{if $b}}{{(index $b 0).HostPort}}{{end}}{{end}}' "$CONTAINER" | head -1)
if [[ -z "$HOST_PORT" ]]; then
  HOST_PORT="8080"
  warn "無法自動偵測 port，預設使用 $HOST_PORT"
else
  ok "Dashboard port: $HOST_PORT"
fi

# ── 4. 直接從 HOST 呼叫（最可靠，繞過所有 proxy）──────────────────────────
echo ""
echo "▶ [4] 從 HOST 直接呼叫 http://localhost:${HOST_PORT}/api/disk-usage"
if command -v curl &>/dev/null; then
  RESP=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:${HOST_PORT}/api/disk-usage" 2>/dev/null)
  BODY=$(curl -s "http://localhost:${HOST_PORT}/api/disk-usage" 2>/dev/null | head -c 200)
  if [[ "$RESP" == "200" ]]; then
    ok "HTTP $RESP → 路由存在，問題在 reverse proxy（nginx 設定）"
    echo "   回應: $BODY"
  elif [[ "$RESP" == "404" ]]; then
    fail "HTTP $RESP → Go binary 沒有此路由，需要重建 image"
    echo "   回應: $BODY"
  else
    warn "HTTP $RESP → 未知狀況"
    echo "   回應: $BODY"
  fi
else
  warn "curl 不可用，嘗試 wget..."
  RESP=$(wget -qO- "http://localhost:${HOST_PORT}/api/disk-usage" 2>/dev/null | head -c 200)
  echo "   回應: $RESP"
fi

# ── 5. 確認 source code 是否存在（最關鍵）──────────────────────────────────
echo ""
echo "▶ [5] 確認 source code 是否包含 disk_usage.go"
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SOURCE_FILE="$SCRIPT_DIR/internal/api/disk_usage.go"
if [[ -f "$SOURCE_FILE" ]]; then
  ok "找到 $SOURCE_FILE"
  # 確認 RegisterDiskUsageRoute 函式存在
  if grep -q "RegisterDiskUsageRoute" "$SOURCE_FILE"; then
    ok "RegisterDiskUsageRoute 函式存在"
  else
    fail "RegisterDiskUsageRoute 函式不存在 → 檔案版本有誤"
  fi
else
  fail "找不到 $SOURCE_FILE → source code 未同步到此伺服器！"
  echo "   → 需要先將 disk_usage.go 複製/拉取到此伺服器，再重新 build"
fi

# ── 6. 確認 main.go 是否有呼叫 RegisterDiskUsageRoute ─────────────────────
echo ""
echo "▶ [6] 確認 cmd/dashboard/main.go 是否呼叫 RegisterDiskUsageRoute"
MAIN_FILE="$SCRIPT_DIR/cmd/dashboard/main.go"
if [[ -f "$MAIN_FILE" ]]; then
  if grep -q "RegisterDiskUsageRoute" "$MAIN_FILE"; then
    ok "main.go 有呼叫 RegisterDiskUsageRoute"
  else
    fail "main.go 沒有呼叫 RegisterDiskUsageRoute → 需要加入此呼叫再重建"
  fi
else
  warn "找不到 $MAIN_FILE"
fi

# ── 7. 確認環境變數 AGENT_URL & 測試 agent ─────────────────────────────────
echo ""
echo "▶ [7] 環境變數 AGENT_URL & agent /disk-usage 測試"
AGENT_URL=$(docker inspect --format '{{range .Config.Env}}{{.}}{{"\n"}}{{end}}' "$CONTAINER" | grep '^AGENT_URL=' | cut -d= -f2-)
if [[ -z "$AGENT_URL" ]]; then
  warn "AGENT_URL 未設定 → disk-usage 直接在容器內執行 df"
else
  ok "AGENT_URL=$AGENT_URL"
  if command -v curl &>/dev/null; then
    AGENT_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${AGENT_URL}/disk-usage" 2>/dev/null)
    AGENT_BODY=$(curl -s "${AGENT_URL}/disk-usage" 2>/dev/null | head -c 200)
    if [[ "$AGENT_STATUS" == "200" ]]; then
      ok "Agent /disk-usage HTTP $AGENT_STATUS → agent 正常"
      echo "   回應: $AGENT_BODY"
    else
      fail "Agent /disk-usage HTTP $AGENT_STATUS"
      echo "   回應: $AGENT_BODY"
    fi
  fi
fi

# ── 8. 最近容器 log ───────────────────────────────────────────────────────────
echo ""
echo "▶ [8] 最近 20 行容器 log"
docker logs --tail 20 "$CONTAINER" 2>&1

# ── 9. 結論 ──────────────────────────────────────────────────────────────────
echo ""
echo "=========================================="
echo " 結論"
echo "=========================================="
if [[ ! -f "$SOURCE_FILE" ]]; then
  fail "source code 未同步！請先執行："
  echo ""
  echo "   git pull   （或手動複製 internal/api/disk_usage.go）"
  echo "   docker compose build dashboard && docker compose up -d"
elif ! grep -q "RegisterDiskUsageRoute" "$MAIN_FILE" 2>/dev/null; then
  fail "main.go 缺少路由呼叫，請確認程式碼後重建"
else
  echo "   source code 完整，請依 [4] 的結果判斷："
  echo "   → HTTP 404 from localhost: docker compose build dashboard && docker compose up -d"
  echo "   → HTTP 200 from localhost: 檢查 nginx/reverse proxy 設定"
fi
