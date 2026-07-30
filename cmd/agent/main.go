package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	agentdiag "backup-manager/internal/agent"
	"backup-manager/internal/api"
	"backup-manager/internal/backup"
	"backup-manager/internal/client"
	"backup-manager/internal/notify"
	"backup-manager/internal/scheduler"
	"backup-manager/internal/store"
)

var buildVersion = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dashURL := requireEnv("DASHBOARD_URL")
	agentCode := requireEnv("AGENT_CODE")
	agentToken := getEnvOr("AGENT_TOKEN", "")
	agentAddr := getEnvOr("AGENT_ADDR", ":9090")

	c := client.New(dashURL, agentCode, agentToken)

	notifier := notify.NewSlack()

	runner := &backup.Runner{
		Store:    c,
		Uploader: c,
		Notifier: notifier,
	}

	sched := scheduler.New(c, runner)
	if err := sched.Start(ctx); err != nil {
		log.Fatalf("[agent] 排程器啟動失敗: %v", err)
	}
	defer sched.Stop()

	commandCh := make(chan store.AgentCommand, 32)
	go startCommandWorker(ctx, c, runner, sched, commandCh)
	go startHeartbeat(ctx, c, commandCh)

	// ── HTTP server（供 dashboard 轉發 trigger）─────────────────────
	mux := http.NewServeMux()

	// 驗證 token
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if agentCode != "" && r.Header.Get("X-Agent-Code") != agentCode {
				http.Error(w, `{"error":"invalid agent code"}`, http.StatusUnauthorized)
				return
			}
			if agentToken != "" && r.Header.Get("X-Agent-Token") != agentToken {
				http.Error(w, `{"error":"invalid agent token"}`, http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	// POST /trigger  {"project_id":1,"target_type":"all"}
	mux.HandleFunc("POST /trigger", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ProjectID  int    `json:"project_id"`
			TargetType string `json:"target_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if req.ProjectID == 0 {
			http.Error(w, `{"error":"project_id required"}`, http.StatusBadRequest)
			return
		}
		if req.TargetType == "" {
			req.TargetType = "all"
		}
		go func() {
			if err := runner.RunProject(context.Background(), req.ProjectID, []string{req.TargetType}, nil, "manual"); err != nil {
				log.Printf("[agent-trigger] project_id=%d type=%s 失敗: %v", req.ProjectID, req.TargetType, err)
			} else {
				log.Printf("[agent-trigger] project_id=%d type=%s 完成", req.ProjectID, req.TargetType)
			}
		}()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"triggered"}`))
	}))

	// POST /test-backup {"project_id":1,"target_type":"all"}
	mux.HandleFunc("POST /test-backup", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ProjectID  int    `json:"project_id"`
			TargetType string `json:"target_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if req.ProjectID == 0 {
			http.Error(w, `{"error":"project_id required"}`, http.StatusBadRequest)
			return
		}
		if req.TargetType == "" {
			req.TargetType = "all"
		}
		go func() {
			err := runner.RunProjectWithOptions(context.Background(), req.ProjectID, []string{req.TargetType}, backup.RunOptions{
				TriggeredBy: "smoke-backup",
				Smoke:       true,
			})
			if err != nil {
				log.Printf("[agent-smoke] project_id=%d type=%s 失敗: %v", req.ProjectID, req.TargetType, err)
				return
			}
			log.Printf("[agent-smoke] project_id=%d type=%s 完成", req.ProjectID, req.TargetType)
		}()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"triggered"}`))
	}))

	// POST /restore {"record_id":1,"strategy":"new|overwrite","target":"/restore/path"}
	mux.HandleFunc("POST /restore", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RecordID int64  `json:"record_id"`
			Strategy string `json:"strategy"`
			Target   string `json:"target"`
			Confirm  string `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if req.RecordID == 0 {
			http.Error(w, `{"error":"record_id required"}`, http.StatusBadRequest)
			return
		}
		if req.Strategy == "" {
			req.Strategy = "overwrite"
		}
		if req.Strategy != "new" && req.Strategy != "overwrite" {
			http.Error(w, `{"error":"invalid strategy"}`, http.StatusBadRequest)
			return
		}
		if req.Strategy == "overwrite" && req.Confirm != "RESTORE" {
			http.Error(w, `{"error":"overwrite requires confirm=RESTORE"}`, http.StatusBadRequest)
			return
		}
		result, err := runRestore(r.Context(), c, req.RecordID, req.Strategy, req.Target)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result) //nolint
	}))

	// GET /healthz
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// GET /api/agent/schedules/enabled
	// 提供本機 smoke test 與診斷工具直接驗證 agent 對 dashboard 的排程讀取能力。
	mux.HandleFunc("GET /api/agent/schedules/enabled", auth(func(w http.ResponseWriter, r *http.Request) {
		schedules, err := c.ListEnabledSchedules(r.Context())
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(schedules) //nolint
	}))

	// GET /ssh-audit  — host 直接查詢 journalctl，不需經過 Docker 容器
	mux.HandleFunc("GET /ssh-audit", auth(api.HandleSSHAuditDirect))

	// GET /disk-usage  — 回傳 host 磁碟分割區使用狀況
	mux.HandleFunc("GET /disk-usage", auth(api.HandleDiskUsageDirect))

	// POST /syslogs/run  — 接收 SyslogConfig JSON，在 host 上執行日誌備份（journalctl）
	mux.HandleFunc("POST /syslogs/run", auth(api.HandleSyslogRunDirect))

	// POST /syslogs/test  — 接收 SyslogConfig JSON，在 host 上執行備份前診斷
	mux.HandleFunc("POST /syslogs/test", auth(api.HandleSyslogTestDirect))

	// POST /gcp/run  — 接收 GcpRunRequest JSON，在 host 上執行 rsync 備份
	mux.HandleFunc("POST /gcp/run", auth(api.HandleGcpRunDirect))

	// POST /gcp/test  — 接收 GcpTestRequest JSON，在 host 上執行診斷（rsync/ssh 可用性）
	mux.HandleFunc("POST /gcp/test", auth(api.HandleGcpTestDirect))

	// POST /test/preflight — 驗證 project path / NAS / DB 條件
	mux.HandleFunc("POST /test/preflight", auth(func(w http.ResponseWriter, r *http.Request) {
		var req agentdiag.PreflightRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		result, err := agentdiag.RunPreflight(r.Context(), c, req)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result) //nolint
	}))

	// GET /releases/capability — 查詢 agent 本機 release build 能力
	mux.HandleFunc("GET /releases/capability", auth(api.HandleAgentReleaseCapabilityDirect))

	// POST /releases/build — 在 agent host 上建立 release artifact
	mux.HandleFunc("POST /releases/build", auth(api.HandleAgentReleaseBuildDirect))

	// GET /releases/{version}/download/{file} — 供 dashboard 拉回 artifact
	mux.HandleFunc("GET /releases/{version}/download/{file}", auth(api.HandleAgentReleaseDownloadDirect))

	// POST /schedules/{id}/reload  — 通知 agent scheduler 重載指定排程
	mux.HandleFunc("POST /schedules/{id}/reload", auth(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
			return
		}
		if err := sched.Reload(context.Background(), id); err != nil {
			log.Printf("[agent] schedule reload id=%d err=%v", id, err)
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[agent] schedule reloaded id=%d", id)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))

	// POST /schedules/{id}/remove  — 通知 agent scheduler 移除指定排程
	mux.HandleFunc("POST /schedules/{id}/remove", auth(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
			return
		}
		sched.Remove(id)
		log.Printf("[agent] schedule removed id=%d", id)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))

	srv := &http.Server{
		Addr:        agentAddr,
		Handler:     api.CORSMiddleware(mux),
		ReadTimeout: 10 * time.Second,
		// Agent 會處理 release build、診斷與 host 任務，回應可能超過 10 秒。
		WriteTimeout: 10 * time.Minute,
	}
	go func() {
		log.Printf("[agent] HTTP server 啟動於 %s", agentAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] HTTP server 錯誤: %v", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	log.Printf("[agent] 啟動完成，dashboard: %s", dashURL)
	log.Printf("[agent] version=%s", agentVersion())
	log.Printf("[agent] AGENT_CODE=%q", agentCode)
	log.Printf("[agent] HOST_PREFIX=%q  NAS_BASE=%q",
		getEnvOr("HOST_PREFIX", ""), getEnvOr("NAS_BASE", "/mnt/nas/backups"))

	<-ctx.Done()
	log.Println("[agent] 收到關閉訊號，正在停止...")
}

func runRestore(ctx context.Context, c *client.DashboardClient, recordID int64, strategy, target string) (map[string]any, error) {
	rec, err := c.GetRecord(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if rec.ProjectID == nil {
		return nil, fmt.Errorf("record missing project_id")
	}
	project, err := c.GetProject(ctx, *rec.ProjectID)
	if err != nil {
		return nil, err
	}
	targets, err := c.ListTargets(ctx, *rec.ProjectID)
	if err != nil {
		return nil, err
	}
	var bt *store.BackupTarget
	if rec.TargetID != nil {
		for i := range targets {
			if targets[i].ID == *rec.TargetID {
				bt = &targets[i]
				break
			}
		}
	}
	if bt == nil {
		return nil, fmt.Errorf("backup target not found")
	}

	tmpDir := filepath.Join(os.TempDir(), "backup-agent-restore")
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("record_%d_%s", recordID, filepath.Base(rec.Filename)))
	if err := c.DownloadBackup(ctx, recordID, tmpPath); err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath) //nolint

	restoreTarget := target
	snapshotPath := ""
	snapshotDir := os.Getenv("BACKUP_AGENT_RESTORE_SNAPSHOT_DIR")
	if snapshotDir == "" {
		snapshotDir = filepath.Join(os.TempDir(), "backup-agent-restore-snapshots")
	}
	switch rec.Type {
	case "files", "system":
		if restoreTarget == "" && strategy == "overwrite" && rec.Type == "files" {
			var cfg backup.FilesConfig
			if err := json.Unmarshal(bt.Config, &cfg); err != nil {
				return nil, err
			}
			restoreTarget = cfg.Source
		}
		if restoreTarget == "" && strategy == "overwrite" && rec.Type == "system" {
			restoreTarget = "/"
		}
		if strategy == "overwrite" {
			path, err := backup.SnapshotFiles(restoreTarget, snapshotDir, rec.ProjectName)
			if err != nil {
				return nil, fmt.Errorf("overwrite 前 snapshot 失敗: %w", err)
			}
			snapshotPath = path
		}
		if err := backup.RestoreFiles(tmpPath, backup.RestoreOptions{Strategy: strategy, Target: restoreTarget}); err != nil {
			return nil, err
		}
	case "database":
		cfg, err := backup.ParseDatabaseConfig(bt.Config)
		if err != nil {
			return nil, err
		}
		if cfg.ContainerName == "" && cfg.Host == "" {
			if project.DockerDbContainer != "" {
				cfg.ContainerName = project.DockerDbContainer
				if cfg.DBType == "" {
					cfg.DBType = project.DbType
				}
				if cfg.Name == "" {
					cfg.Name = project.DbName
				}
				if cfg.User == "" {
					cfg.User = project.DbUser
				}
				if cfg.PasswordEnv == "" {
					cfg.PasswordEnv = project.DbPasswordEnv
				}
				if cfg.Password == "" {
					cfg.Password = project.DbPassword
				}
			} else if project.DbHost != "" {
				cfg.Host = project.DbHost
				cfg.Port = project.DbPort
				if cfg.DBType == "" {
					cfg.DBType = project.DbType
				}
				if cfg.Name == "" {
					cfg.Name = project.DbName
				}
				if cfg.User == "" {
					cfg.User = project.DbUser
				}
				if cfg.PasswordEnv == "" {
					cfg.PasswordEnv = project.DbPasswordEnv
				}
				if cfg.Password == "" {
					cfg.Password = project.DbPassword
				}
			}
		}
		restoreCfg := *cfg
		if restoreTarget != "" {
			restoreCfg.Name = restoreTarget
		}
		if strategy == "overwrite" {
			path, err := backup.SnapshotDatabase(&restoreCfg, snapshotDir, rec.ProjectName)
			if err != nil {
				return nil, fmt.Errorf("overwrite 前 DB snapshot 失敗: %w", err)
			}
			snapshotPath = path
		}
		if strategy == "overwrite" && restoreCfg.DBType == "postgres" {
			// 仿 dashboard apply-backup:先重建目標資料庫再灌入 dump,
			// 避免 plain dump 撞到既有資料表;失敗時以 snapshot 回復。
			if _, _, err := backup.ReplacePostgresDatabase(tmpPath, &restoreCfg, snapshotPath); err != nil {
				return nil, err
			}
		} else if err := backup.RestoreDatabase(tmpPath, cfg, backup.RestoreOptions{Strategy: strategy, Target: restoreTarget}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported restore type: %s", rec.Type)
	}

	return map[string]any{
		"status":        "restored",
		"record_id":     recordID,
		"type":          rec.Type,
		"strategy":      strategy,
		"target":        restoreTarget,
		"snapshot_path": snapshotPath,
	}, nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("環境變數 %s 未設定", key)
	}
	return v
}

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func startHeartbeat(ctx context.Context, c *client.DashboardClient, commandCh chan<- store.AgentCommand) {
	send := func() {
		host, _ := os.Hostname()
		hb := store.AgentHeartbeat{
			HostName:  host,
			IPAddress: getEnvOr("AGENT_IP_ADDRESS", ""),
			Version:   agentVersion(),
			LastError: "",
		}
		diskUsage, err := api.CollectDiskUsageSnapshot()
		if err != nil {
			log.Printf("[agent] 收集磁碟資訊失敗: %v", err)
		} else {
			hb.DiskUsage = diskUsage
		}
		commands, err := c.Heartbeat(ctx, hb)
		if err != nil {
			log.Printf("[agent] heartbeat 失敗: %v", err)
			return
		}
		for _, cmd := range commands {
			select {
			case commandCh <- cmd:
				log.Printf("[agent] queued command id=%d type=%s", cmd.ID, cmd.Type)
			case <-ctx.Done():
				return
			}
		}
	}

	send()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func agentVersion() string {
	if v := getEnvOr("AGENT_VERSION", ""); v != "" && v != "dev" {
		return v
	}
	if buildVersion != "" {
		return buildVersion
	}
	return "dev"
}

func startCommandWorker(ctx context.Context, c *client.DashboardClient, runner *backup.Runner, sched *scheduler.DynamicScheduler, commandCh <-chan store.AgentCommand) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-commandCh:
			executeAgentCommand(context.Background(), c, runner, sched, cmd)
		}
	}
}

func executeAgentCommand(ctx context.Context, c *client.DashboardClient, runner *backup.Runner, sched *scheduler.DynamicScheduler, cmd store.AgentCommand) {
	var logBuf bytes.Buffer
	logf := func(format string, args ...any) {
		fmt.Fprintf(&logBuf, "%s ", time.Now().Format(time.RFC3339))
		fmt.Fprintf(&logBuf, format, args...)
		logBuf.WriteByte('\n')
	}

	logf("command start id=%d type=%s", cmd.ID, cmd.Type)

	status := store.AgentCommandStatusSuccess
	result := json.RawMessage(`{}`)
	logRef := ""
	errorMsg := ""

	switch cmd.Type {
	case store.AgentCommandTypeTriggerBackup:
		var payload api.TriggerBackupCommandPayload
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = "invalid trigger payload: " + err.Error()
			logf("%s", errorMsg)
			break
		}
		if payload.TargetType == "" {
			payload.TargetType = "all"
		}
		logf("trigger backup project_id=%d target_type=%s smoke=%t", payload.ProjectID, payload.TargetType, payload.Smoke)
		var err error
		if payload.Smoke {
			err = runner.RunProjectWithOptions(ctx, payload.ProjectID, []string{payload.TargetType}, backup.RunOptions{
				TriggeredBy: "smoke-backup",
				Smoke:       true,
			})
		} else {
			err = runner.RunProject(ctx, payload.ProjectID, []string{payload.TargetType}, nil, "manual")
		}
		if err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = err.Error()
			logf("trigger backup failed: %v", err)
		} else {
			logf("trigger backup success")
			result = mustJSON(map[string]any{
				"project_id":  payload.ProjectID,
				"target_type": payload.TargetType,
				"smoke":       payload.Smoke,
			})
		}

	case store.AgentCommandTypeRestoreBackup:
		var payload api.RestoreBackupCommandPayload
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = "invalid restore payload: " + err.Error()
			logf("%s", errorMsg)
			break
		}
		logf("restore start record_id=%d strategy=%s target=%q restore_id=%d", payload.RecordID, payload.Strategy, payload.Target, payload.RestoreID)
		out, err := runRestore(ctx, c, payload.RecordID, payload.Strategy, payload.Target)
		if err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = err.Error()
			logf("restore failed: %v", err)
		} else {
			logf("restore success")
			result = mustJSON(out)
		}

	case store.AgentCommandTypeReloadSchedule:
		var payload api.ScheduleCommandPayload
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = "invalid reload payload: " + err.Error()
			logf("%s", errorMsg)
			break
		}
		logf("reload schedule schedule_id=%d", payload.ScheduleID)
		if err := sched.Reload(ctx, payload.ScheduleID); err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = err.Error()
			logf("reload schedule failed: %v", err)
		} else {
			result = mustJSON(map[string]any{"schedule_id": payload.ScheduleID, "action": "reload"})
			logf("reload schedule success")
		}

	case store.AgentCommandTypeRemoveSchedule:
		var payload api.ScheduleCommandPayload
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			status = store.AgentCommandStatusFailed
			errorMsg = "invalid remove payload: " + err.Error()
			logf("%s", errorMsg)
			break
		}
		logf("remove schedule schedule_id=%d", payload.ScheduleID)
		sched.Remove(payload.ScheduleID)
		result = mustJSON(map[string]any{"schedule_id": payload.ScheduleID, "action": "remove"})
		logf("remove schedule success")

	default:
		status = store.AgentCommandStatusFailed
		errorMsg = "unsupported command type: " + cmd.Type
		logf("%s", errorMsg)
	}

	logf("command finish status=%s", status)
	if err := c.FinishAgentCommand(ctx, cmd.ID, status, result, logBuf.String(), logRef, errorMsg); err != nil {
		log.Printf("[agent] finish command id=%d err=%v", cmd.ID, err)
		return
	}
	log.Printf("[agent] command finished id=%d type=%s status=%s", cmd.ID, cmd.Type, status)
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
