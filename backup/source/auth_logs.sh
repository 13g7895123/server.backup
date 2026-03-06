#!/bin/bash

# 設定日期格式
DATE=$(date +%Y-%m-%d)

# 備份目的地
DEST="/mnt/nas/backup/os/auth"

# 建立目的地資料夾（以日期區分）
mkdir -p "$DEST/$DATE"

# 優先透過 journalctl 匯出 auth 等效日誌
# SYSLOG_FACILITY=4  (auth)     → 一般認證訊息
# SYSLOG_FACILITY=10 (authpriv) → SSH 登入、sudo、PAM（同欄位多值為 OR）
if command -v journalctl &>/dev/null; then
    journalctl \
        SYSLOG_FACILITY=4 SYSLOG_FACILITY=10 \
        --since "${DATE} 00:00:00" \
        --until "${DATE} 23:59:59" \
        -o short-iso --no-pager \
        | gzip > "$DEST/$DATE/auth_${DATE}.gz"
    echo "[auth_logs] journalctl 匯出完成: $DEST/$DATE/auth_${DATE}.gz"
else
    # Fallback：直接複製 /var/log/auth.log 傳統日誌檔
    LOG_FILE="/var/log/auth.log"
    if [ -f "$LOG_FILE" ]; then
        BASENAME=$(basename "$LOG_FILE")
        cp "$LOG_FILE" "$DEST/$DATE/${BASENAME}_${DATE}"
        gzip "$DEST/$DATE/${BASENAME}_${DATE}"
        echo "[auth_logs] 檔案複製完成 (fallback): $DEST/$DATE/${BASENAME}_${DATE}.gz"
    else
        echo "[auth_logs] 錯誤：journalctl 不可用且 $LOG_FILE 不存在" >&2
        exit 1
    fi
fi
