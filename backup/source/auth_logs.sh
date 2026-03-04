#!/bin/bash

# 設定日期格式
DATE=$(date +%Y-%m-%d)

# 要備份的日誌檔案
LOG_FILES="/var/log/auth.log"

# 備份目的地
DEST="/mnt/nas/backup/os/auth"

# 建立目的地資料夾（以日期區分）
mkdir -p "$DEST/$DATE"

# 複製並壓縮日誌
for LOG in $LOG_FILES; do
    BASENAME=$(basename $LOG)
    cp $LOG "$DEST/$DATE/${BASENAME}_$DATE"
#    gzip "$DEST/$DATE/${BASENAME}_$DATE"
done
