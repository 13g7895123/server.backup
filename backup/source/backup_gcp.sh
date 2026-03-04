#!/bin/bash

# === 設定區 ===
DATE=$(date +%F)
BACKUP_DIR="/mnt/nas/backup/project/professional/$DATE"
BACKUP_DB_DIR="/mnt/nas/backup/database/professional/$DATE"
REMOTE_USER="backupuser"
REMOTE_HOST="104.199.148.199"
REMOTE_PATH="/home/backupuser/backup/project/$DATE"
REMOTE_DB_PATH="/home/backupuser/backup/database/$DATE"
SSH_KEY="/home/chinchungtu/.ssh/id_rsa_backup_gcp"
#DB_USER="root"
#DB_PASS="yourpassword"
#DB_NAME="your_database"

# === 建立本地備份資料夾 ===
#mkdir -p "$BACKUP_DIR"

# === 備份資料夾 ===
#tar -czf "$BACKUP_DIR/project.tar.gz" /var/www/your_project_folder

# === 備份資料庫 ===
#mysqldump -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" > "$BACKUP_DIR/db.sql"

# === 傳送到 GCP VPS ===
rsync -avz -e "ssh -i $SSH_KEY" "$BACKUP_DIR/" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_PATH"
rsync -avz -e "ssh -i $SSH_KEY" "$BACKUP_DB_DIR/" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DB_PATH"


# === 保留 7 天的本地備份 ===
#find /opt/backups/ -type d -mtime +7 -exec rm -rf {} \;
