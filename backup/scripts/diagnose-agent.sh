#!/usr/bin/env bash
# scripts/diagnose-agent.sh
# 診斷 backup-agent 常見問題：token、URL、連線、container 狀態
# 需要以 root 執行（或有 docker exec 權限）
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="/etc/backup-agent/env"
PROJ_ENV="${ROOT}/.env"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "  ${GREEN}[OK]${NC}  $*"; }
warn() { echo -e "  ${YELLOW}[WARN]${NC} $*"; }
fail() { echo -e "  ${RED}[FAIL]${NC} $*"; }

ERRORS=0

echo "======================================"
echo " backup-agent 診斷工具"
echo "======================================"

# ── 1. 讀取 agent env ─────────────────────────────────────────────
echo ""
echo "【1】Agent 環境設定 ($ENV_FILE)"

if [[ ! -f "$ENV_FILE" ]]; then
  fail "找不到 $ENV_FILE，尚未安裝 agent"
  ERRORS=$((ERRORS+1))
else
  ok "設定檔存在"

  # 解析時去除行內注解與空白（模擬 systemd EnvironmentFile 行為）
  _parse() { grep -E "^$1=" "$ENV_FILE" | head -1 | sed 's/#.*//' | cut -d= -f2- | xargs; }

  AGENT_URL=$(_parse DASHBOARD_URL)
  AGENT_TOKEN=$(_parse AGENT_TOKEN)

  echo "  DASHBOARD_URL = ${AGENT_URL:-(未設定)}"
  echo "  AGENT_TOKEN   = ${AGENT_TOKEN:-(未設定，表示不驗證)}"

  if [[ -z "$AGENT_URL" ]]; then
    fail "DASHBOARD_URL 未設定"
    ERRORS=$((ERRORS+1))
  fi
fi

# ── 2. 讀取 docker .env ───────────────────────────────────────────
echo ""
echo "【2】Dashboard .env ($PROJ_ENV)"

if [[ ! -f "$PROJ_ENV" ]]; then
  warn "$PROJ_ENV 不存在（若 dashboard 跑在其他主機可忽略）"
else
  ok "設定檔存在"
  _parse_env() { grep -E "^$1=" "$PROJ_ENV" | head -1 | sed 's/#.*//' | cut -d= -f2- | xargs; }

  DASH_TOKEN=$(_parse_env AGENT_TOKEN)
  DASH_PORT=$(_parse_env DASHBOARD_PORT)

  echo "  AGENT_TOKEN    = ${DASH_TOKEN:-(未設定，表示不驗證)}"
  echo "  DASHBOARD_PORT = ${DASH_PORT:-(未設定)}"
fi

# ── 3. Token 比對 ─────────────────────────────────────────────────
echo ""
echo "【3】Token 比對"

if [[ -n "${AGENT_TOKEN:-}" && -n "${DASH_TOKEN:-}" ]]; then
  if [[ "$AGENT_TOKEN" == "$DASH_TOKEN" ]]; then
    ok "兩邊 token 一致：${AGENT_TOKEN}"
  else
    fail "Token 不符！"
    echo "  agent env   : '${AGENT_TOKEN}'"
    echo "  .env (docker): '${DASH_TOKEN}'"
    echo ""
    echo "  修正方法："
    echo "    sed -i \"s/^AGENT_TOKEN=.*/AGENT_TOKEN=${AGENT_TOKEN}/\" \"${PROJ_ENV}\""
    echo "    然後重建 container：docker compose up -d --force-recreate dashboard"
    ERRORS=$((ERRORS+1))
  fi
else
  warn "跳過 token 比對（其中一方未設定）"
fi

# ── 4. Dashboard container 內的 token ────────────────────────────
echo ""
echo "【4】Dashboard container 實際環境變數"

COMPOSE_FILE="${ROOT}/docker-compose.yml"
if [[ ! -f "$COMPOSE_FILE" ]]; then
  warn "找不到 $COMPOSE_FILE，跳過 container 檢查"
else
  CONTAINER_TOKEN=$(docker compose -f "$COMPOSE_FILE" exec -T dashboard env 2>/dev/null \
    | grep '^AGENT_TOKEN=' | cut -d= -f2- | xargs || true)

  if [[ -z "$CONTAINER_TOKEN" ]]; then
    warn "無法取得 container 內的 AGENT_TOKEN（container 可能未啟動）"
  else
    echo "  container AGENT_TOKEN = '${CONTAINER_TOKEN}'"
    if [[ -n "${AGENT_TOKEN:-}" && "$AGENT_TOKEN" != "$CONTAINER_TOKEN" ]]; then
      fail "Container 內的 token 與 agent env 不符！container 可能尚未重建"
      echo "  執行：docker compose -f \"${COMPOSE_FILE}\" up -d --force-recreate dashboard"
      ERRORS=$((ERRORS+1))
    elif [[ -n "${AGENT_TOKEN:-}" ]]; then
      ok "Container token 與 agent env 一致"
    fi
  fi
fi

# ── 5. 連線測試 ───────────────────────────────────────────────────
echo ""
echo "【5】連線測試"

if [[ -z "${AGENT_URL:-}" ]]; then
  warn "DASHBOARD_URL 未知，跳過連線測試"
else
  HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
    -H "X-Agent-Token: ${AGENT_TOKEN:-}" \
    "${AGENT_URL}/api/agent/schedules/enabled" 2>/dev/null || echo "FAIL")

  case "$HTTP_STATUS" in
    200)
      ok "API 回應 200，連線與 token 均正常" ;;
    401)
      fail "API 回應 401：token 錯誤（container 可能還在用舊設定）" 
      ERRORS=$((ERRORS+1)) ;;
    000|FAIL)
      fail "無法連線至 $AGENT_URL（connection refused 或 URL 錯誤）"
      ERRORS=$((ERRORS+1)) ;;
    *)
      warn "API 回應 HTTP $HTTP_STATUS" ;;
  esac
fi

# ── 6. systemd service 狀態 ───────────────────────────────────────
echo ""
echo "【6】systemd service 狀態"

if systemctl is-active --quiet backup-agent 2>/dev/null; then
  ok "backup-agent.service 正在運行"
else
  SVC_STATE=$(systemctl show backup-agent --property=ActiveState --value 2>/dev/null || echo "unknown")
  warn "backup-agent.service 狀態：${SVC_STATE}"
  echo "  最後幾行 log："
  journalctl -u backup-agent -n 5 --no-pager 2>/dev/null | sed 's/^/    /'
fi

# ── 7. 結論 ───────────────────────────────────────────────────────
echo ""
echo "======================================"
if [[ $ERRORS -eq 0 ]]; then
  echo -e "  ${GREEN}全部檢查通過！${NC}"
else
  echo -e "  ${RED}發現 $ERRORS 個問題，請依上方提示修正${NC}"
fi
echo "======================================"
