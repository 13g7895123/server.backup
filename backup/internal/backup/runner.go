package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"backup-manager/internal/notify"
	"backup-manager/internal/store"
)

// BackingStore 是 runner 需要的底層儲存介面。
// store.Store 和 client.DashboardClient 都實作此介面。
type BackingStore interface {
	GetProject(ctx context.Context, id int) (*store.Project, error)
	ListTargets(ctx context.Context, projectID int) ([]store.BackupTarget, error)
	ListRetention(ctx context.Context, projectID int) ([]store.RetentionPolicy, error)
	CreateRecord(ctx context.Context, r *store.BackupRecord) (int64, error)
	UpdateRecord(ctx context.Context, r *store.BackupRecord) error
	ListRecords(ctx context.Context, f store.ListRecordsFilter) ([]store.BackupRecord, int64, error)
	DeleteRecord(ctx context.Context, id int64) (string, error)
}

// Runner 執行備份並寫入紀錄
type Runner struct {
	Store    BackingStore
	Notifier *notify.Slack
}

// RunTarget 執行單一 backup target，寫入 backup_records
func (r *Runner) RunTarget(ctx context.Context, proj *store.Project, target *store.BackupTarget, scheduleID *int, triggeredBy string) error {
	timestamp := time.Now().Format("20060102_150405")
	date := time.Now().Format("2006-01-02")
	destDir := filepath.Join(proj.NasBase, "projects", proj.Name, target.Type, date)

	filename := fmt.Sprintf("%s_%s_%s.tar.gz", proj.Name, target.Type, timestamp)
	if target.Type == "database" {
		filename = fmt.Sprintf("%s_%s_%s.sql.gz", proj.Name, target.Type, timestamp)
	}
	if target.Type == "system" {
		filename = fmt.Sprintf("%s_system_%s.tar.gz", proj.Name, timestamp)
	}

	destPath := filepath.Join(destDir, filename)

	// 建立執行中紀錄
	rec := &store.BackupRecord{
		ProjectID:   &proj.ID,
		ProjectName: proj.Name,
		TargetID:    &target.ID,
		ScheduleID:  scheduleID,
		Type:        target.Type,
		Label:       target.Label,
		Filename:    filename,
		Path:        destPath,
		TriggeredBy: triggeredBy,
	}

	recID, err := r.Store.CreateRecord(ctx, rec)
	if err != nil {
		return fmt.Errorf("建立備份紀錄失敗: %w", err)
	}
	rec.ID = recID

	start := time.Now()
	var checksum string
	var size int64
	var backupErr error

	switch target.Type {
	case "files":
		var cfg FilesConfig
		if err := json.Unmarshal(target.Config, &cfg); err != nil {
			backupErr = fmt.Errorf("解析 files config 失敗: %w", err)
		} else {
			checksum, size, backupErr = BackupFiles(cfg, destPath)
		}

	case "database":
		cfg, err := ParseDatabaseConfig(target.Config)
		if err != nil {
			backupErr = fmt.Errorf("解析 database config 失敗: %w", err)
		} else {
			// 若 target config 未設定連線資訊，自動套用 project-level 設定
			if cfg.ContainerName == "" && cfg.Host == "" {
				if proj.DockerDbContainer != "" {
					cfg.ContainerName = proj.DockerDbContainer
					if cfg.DBType == "" {
						cfg.DBType = proj.DbType
					}
					if cfg.Name == "" {
						cfg.Name = proj.DbName
					}
					if cfg.User == "" {
						cfg.User = proj.DbUser
					}
					if cfg.PasswordEnv == "" {
						cfg.PasswordEnv = proj.DbPasswordEnv
					}
				} else if proj.DbHost != "" {
					cfg.Host = proj.DbHost
					cfg.Port = proj.DbPort
					if cfg.DBType == "" {
						cfg.DBType = proj.DbType
					}
					if cfg.Name == "" {
						cfg.Name = proj.DbName
					}
					if cfg.User == "" {
						cfg.User = proj.DbUser
					}
					if cfg.PasswordEnv == "" {
						cfg.PasswordEnv = proj.DbPasswordEnv
					}
				}
			}
			rec.SubType = cfg.DBType
			checksum, size, backupErr = BackupDatabase(cfg, destPath)
		}

	case "system":
		cfg, err := ParseSystemConfig(target.Config)
		if err != nil {
			backupErr = fmt.Errorf("解析 system config 失敗: %w", err)
		} else {
			rec.SubType = "debian"
			checksum, size, backupErr = BackupSystem(cfg, destDir, timestamp)
		}

	default:
		backupErr = fmt.Errorf("未知備份類型: %s", target.Type)
	}

	duration := time.Since(start).Seconds()

	// 計算保留期限
	retainedUntil := computeRetainedUntil(proj.ID, target.Type, r.Store, ctx)

	// 更新紀錄
	rec.SizeBytes = size
	rec.Checksum = checksum
	rec.DurationSec = duration
	rec.RetainedUntil = retainedUntil
	if backupErr != nil {
		rec.Status = "failed"
		rec.ErrorMsg = backupErr.Error()
	} else {
		rec.Status = "success"
	}

	r.Store.UpdateRecord(ctx, rec) //nolint

	// 發送通知
	if r.Notifier != nil {
		if backupErr != nil {
			r.Notifier.SendFailure(proj.Name, target.Type, backupErr.Error())
		}
	}

	if backupErr != nil {
		return backupErr
	}

	fmt.Printf("[backup] ✓ %s/%s → %s (%.1fs, %.2f MB)\n",
		proj.Name, target.Type, filename, duration, float64(size)/1024/1024)
	return nil
}

// RunProject 執行一個專案下所有 enabled targets（依 targetTypes 篩選）
func (r *Runner) RunProject(ctx context.Context, projectID int, targetTypes []string, scheduleID *int, triggeredBy string) error {
	proj, err := r.Store.GetProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("找不到專案 id=%d: %w", projectID, err)
	}

	targets, err := r.Store.ListTargets(ctx, projectID)
	if err != nil {
		return err
	}

	typeSet := make(map[string]struct{})
	for _, t := range targetTypes {
		typeSet[t] = struct{}{}
	}
	all := len(typeSet) == 0 || contains(targetTypes, "all")

	var lastErr error
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		if !all {
			if _, ok := typeSet[t.Type]; !ok {
				continue
			}
		}
		t := t
		if err := r.RunTarget(ctx, proj, &t, scheduleID, triggeredBy); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func computeRetainedUntil(projectID int, targetType string, s BackingStore, ctx context.Context) *time.Time {
	policies, err := s.ListRetention(ctx, projectID)
	if err != nil {
		return nil
	}
	// 優先找 targetType 精確匹配，否則找 'all'
	var keepDays int
	for _, p := range policies {
		if p.TargetType == targetType {
			keepDays = p.KeepDaily
			break
		}
		if p.TargetType == "all" {
			keepDays = p.KeepDaily
		}
	}
	if keepDays == 0 {
		keepDays = 7
	}
	t := time.Now().AddDate(0, 0, keepDays)
	return &t
}

// DeleteExpiredBackups 清除過期備份檔案與紀錄
func (r *Runner) DeleteExpiredBackups(ctx context.Context) {
	records, _, err := r.Store.ListRecords(ctx, store.ListRecordsFilter{
		Status: "success",
		Limit:  1000,
	})
	if err != nil {
		return
	}
	now := time.Now()
	for _, rec := range records {
		if rec.RetainedUntil != nil && rec.RetainedUntil.Before(now) {
			path, err := r.Store.DeleteRecord(ctx, rec.ID)
			if err == nil && path != "" {
				os.Remove(path)
				fmt.Printf("[cleanup] 刪除過期備份: %s\n", path)
			}
		}
	}
}
