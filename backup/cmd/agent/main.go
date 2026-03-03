package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backup-manager/internal/backup"
	"backup-manager/internal/client"
	"backup-manager/internal/notify"
	"backup-manager/internal/scheduler"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dashURL := requireEnv("DASHBOARD_URL")
	agentToken := getEnvOr("AGENT_TOKEN", "")
	agentAddr := getEnvOr("AGENT_ADDR", ":9090")

	c := client.New(dashURL, agentToken)

	notifier := notify.NewSlack()

	runner := &backup.Runner{
		Store:    c,
		Notifier: notifier,
	}

	sched := scheduler.New(c, runner)
	if err := sched.Start(ctx); err != nil {
		log.Fatalf("[agent] 排程器啟動失敗: %v", err)
	}
	defer sched.Stop()

	// ── HTTP server（供 dashboard 轉發 trigger）─────────────────────
	mux := http.NewServeMux()

	// 驗證 token
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
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

	// GET /healthz
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:         agentAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("[agent] HTTP server 啟動於 %s", agentAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] HTTP server 錯誤: %v", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	log.Printf("[agent] 啟動完成，dashboard: %s", dashURL)
	log.Printf("[agent] HOST_PREFIX=%q  NAS_BASE=%q",
		getEnvOr("HOST_PREFIX", ""), getEnvOr("NAS_BASE", "/mnt/nas/backups"))

	<-ctx.Done()
	log.Println("[agent] 收到關閉訊號，正在停止...")
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
