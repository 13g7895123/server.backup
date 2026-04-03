package api

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"backup-manager/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
)

// SyslogGcpScheduler 管理 syslog_configs 與 gcp_configs 的 cron 排程。
// 從資料庫讀取 cron_expr，在排程時間自動執行備份。
type SyslogGcpScheduler struct {
	pool       *pgxpool.Pool
	store      *store.Store
	cron       *cron.Cron
	syslogJobs map[int]cron.EntryID
	gcpJobs    map[int]cron.EntryID
	mu         sync.Mutex
}

// NewSyslogGcpScheduler 建立排程器實例。
func NewSyslogGcpScheduler(pool *pgxpool.Pool, s *store.Store) *SyslogGcpScheduler {
	return &SyslogGcpScheduler{
		pool:       pool,
		store:      s,
		cron:       cron.New(),
		syslogJobs: make(map[int]cron.EntryID),
		gcpJobs:    make(map[int]cron.EntryID),
	}
}

// Start 讀取所有啟用且有 cron 表達式的設定，並啟動排程器。
func (sch *SyslogGcpScheduler) Start(ctx context.Context) error {
	sch.mu.Lock()
	defer sch.mu.Unlock()

	// ── 載入 syslog 排程 ──
	rows, err := sch.pool.Query(ctx,
		`SELECT id, cron_expr FROM syslog_configs
		 WHERE enabled = true AND cron_expr IS NOT NULL AND cron_expr != ''
		 ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int
		var cronExpr string
		if err := rows.Scan(&id, &cronExpr); err != nil {
			continue
		}
		if err := sch.addSyslogJobLocked(id, cronExpr); err != nil {
			log.Printf("[syslog-sched] 無效 cron %q id=%d: %v", cronExpr, id, err)
		}
	}
	rows.Close()

	// ── 載入 GCP 排程 ──
	rows, err = sch.pool.Query(ctx,
		`SELECT id, cron_expr FROM gcp_configs
		 WHERE enabled = true AND cron_expr IS NOT NULL AND cron_expr != ''
		 ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int
		var cronExpr string
		if err := rows.Scan(&id, &cronExpr); err != nil {
			continue
		}
		if err := sch.addGcpJobLocked(id, cronExpr); err != nil {
			log.Printf("[gcp-sched] 無效 cron %q id=%d: %v", cronExpr, id, err)
		}
	}
	rows.Close()

	sch.cron.Start()
	log.Printf("[syslog-gcp-sched] 已啟動，syslog=%d gcp=%d", len(sch.syslogJobs), len(sch.gcpJobs))
	return nil
}

// Stop 停止排程器。
func (sch *SyslogGcpScheduler) Stop() {
	sch.cron.Stop()
}

// ReloadSyslog 重新載入指定 syslog 設定的 cron 排程（設定/切換後呼叫）。
func (sch *SyslogGcpScheduler) ReloadSyslog(id int) {
	sch.mu.Lock()
	defer sch.mu.Unlock()

	if entryID, ok := sch.syslogJobs[id]; ok {
		sch.cron.Remove(entryID)
		delete(sch.syslogJobs, id)
	}

	var cronExpr string
	var enabled bool
	err := sch.pool.QueryRow(context.Background(),
		`SELECT enabled, COALESCE(cron_expr,'') FROM syslog_configs WHERE id=$1`, id).
		Scan(&enabled, &cronExpr)
	if err != nil || !enabled || cronExpr == "" {
		return
	}

	if err := sch.addSyslogJobLocked(id, cronExpr); err != nil {
		log.Printf("[syslog-sched] ReloadSyslog id=%d 無效 cron %q: %v", id, cronExpr, err)
	} else {
		log.Printf("[syslog-sched] ReloadSyslog id=%d cron=%q", id, cronExpr)
	}
}

// ReloadGcp 重新載入指定 GCP 設定的 cron 排程。
func (sch *SyslogGcpScheduler) ReloadGcp(id int) {
	sch.mu.Lock()
	defer sch.mu.Unlock()

	if entryID, ok := sch.gcpJobs[id]; ok {
		sch.cron.Remove(entryID)
		delete(sch.gcpJobs, id)
	}

	var cronExpr string
	var enabled bool
	err := sch.pool.QueryRow(context.Background(),
		`SELECT enabled, COALESCE(cron_expr,'') FROM gcp_configs WHERE id=$1`, id).
		Scan(&enabled, &cronExpr)
	if err != nil || !enabled || cronExpr == "" {
		return
	}

	if err := sch.addGcpJobLocked(id, cronExpr); err != nil {
		log.Printf("[gcp-sched] ReloadGcp id=%d 無效 cron %q: %v", id, cronExpr, err)
	} else {
		log.Printf("[gcp-sched] ReloadGcp id=%d cron=%q", id, cronExpr)
	}
}

// RemoveSyslog 移除指定 syslog 設定的 cron 排程（刪除設定後呼叫）。
func (sch *SyslogGcpScheduler) RemoveSyslog(id int) {
	sch.mu.Lock()
	defer sch.mu.Unlock()
	if entryID, ok := sch.syslogJobs[id]; ok {
		sch.cron.Remove(entryID)
		delete(sch.syslogJobs, id)
	}
}

// RemoveGcp 移除指定 GCP 設定的 cron 排程。
func (sch *SyslogGcpScheduler) RemoveGcp(id int) {
	sch.mu.Lock()
	defer sch.mu.Unlock()
	if entryID, ok := sch.gcpJobs[id]; ok {
		sch.cron.Remove(entryID)
		delete(sch.gcpJobs, id)
	}
}

// ── 內部輔助 ─────────────────────────────────────────────────────────────────

func (sch *SyslogGcpScheduler) addSyslogJobLocked(id int, cronExpr string) error {
	entryID, err := sch.cron.AddFunc(cronExpr, func() {
		sch.runSyslogJob(id)
	})
	if err != nil {
		return err
	}
	sch.syslogJobs[id] = entryID
	return nil
}

func (sch *SyslogGcpScheduler) addGcpJobLocked(id int, cronExpr string) error {
	entryID, err := sch.cron.AddFunc(cronExpr, func() {
		sch.runGcpJob(id)
	})
	if err != nil {
		return err
	}
	sch.gcpJobs[id] = entryID
	return nil
}

// ── Syslog 執行 ──────────────────────────────────────────────────────────────

func (sch *SyslogGcpScheduler) runSyslogJob(id int) {
	ctx := context.Background()

	var c SyslogConfig
	err := sch.pool.QueryRow(ctx, `
		SELECT id, name, log_type, source_type, log_files, journal_units, journal_format,
		       dest, compress, enabled, cron_expr, last_run_at, run_status, run_message,
		       created_at, updated_at
		FROM syslog_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.LogType, &c.SourceType, &c.LogFiles,
			&c.JournalUnits, &c.JournalFormat,
			&c.Dest, &c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt,
			&c.RunStatus, &c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		log.Printf("[syslog-sched] id=%d 查詢失敗: %v", id, err)
		return
	}
	if !c.Enabled {
		return
	}

	log.Printf("[syslog-sched] 觸發排程 id=%d %q", id, c.Name)
	sch.pool.Exec(ctx, //nolint
		`UPDATE syslog_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)

	rec := &store.BackupRecord{
		ProjectName: c.Name,
		Type:        "syslog",
		SubType:     strconv.Itoa(c.ID),
		Label:       c.Name,
		Filename:    time.Now().Format("2006-01-02") + "/",
		Path:        c.Dest,
		TriggeredBy: "schedule",
	}
	recID, _ := sch.store.CreateRecord(ctx, rec)
	start := time.Now()

	var msg string
	var size int64
	var runErr error
	if c.SourceType == "journal" {
		if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
			msg, size, runErr = proxySyslogRun(agentURL, c)
		} else {
			msg, size, runErr = executeSyslogBackup(c)
		}
	} else {
		msg, size, runErr = executeSyslogBackup(c)
	}

	status := "success"
	errStr := ""
	if runErr != nil {
		status = "failed"
		msg = runErr.Error()
		errStr = msg
		log.Printf("[syslog-sched] id=%d 失敗: %v", id, runErr)
	} else {
		log.Printf("[syslog-sched] id=%d 完成: %s", id, msg)
	}

	now := time.Now()
	sch.pool.Exec(ctx, //nolint
		`UPDATE syslog_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
		status, msg, now, id)

	if recID > 0 {
		rec.ID = recID
		rec.Status = status
		rec.SizeBytes = size
		rec.DurationSec = time.Since(start).Seconds()
		rec.ErrorMsg = errStr
		sch.store.UpdateRecord(ctx, rec) //nolint
	}
}

// ── GCP 執行 ─────────────────────────────────────────────────────────────────

func (sch *SyslogGcpScheduler) runGcpJob(id int) {
	ctx := context.Background()

	var c GcpConfig
	err := sch.pool.QueryRow(ctx, `
		SELECT id, name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled,
		       cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		FROM gcp_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.ProjectIDs, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		log.Printf("[gcp-sched] id=%d 查詢失敗: %v", id, err)
		return
	}
	if !c.Enabled {
		return
	}
	if c.ProjectIDs == nil {
		c.ProjectIDs = []int{}
	}

	log.Printf("[gcp-sched] 觸發排程 id=%d %q", id, c.Name)
	sch.pool.Exec(ctx, //nolint
		`UPDATE gcp_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)

	var msg string
	var runErr error
	if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
		tasks, terr := resolveGcpRsyncTasks(ctx, sch.pool, c)
		if terr != nil {
			runErr = terr
		} else {
			runReq := GcpRunRequest{
				SshKey:     c.SshKey,
				RemoteUser: c.RemoteUser,
				RemoteHost: c.RemoteHost,
				Tasks:      tasks,
			}
			msg, runErr = proxyGcpRun(agentURL, runReq)
		}
	} else {
		msg, runErr = executeGcpBackup(ctx, sch.pool, c)
	}

	status := "success"
	if runErr != nil {
		status = "failed"
		msg = runErr.Error()
		log.Printf("[gcp-sched] id=%d 失敗: %v", id, runErr)
	} else {
		log.Printf("[gcp-sched] id=%d 完成: %s", id, msg)
	}

	now := time.Now()
	sch.pool.Exec(ctx, //nolint
		`UPDATE gcp_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
		status, msg, now, id)
}
