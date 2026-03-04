package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backup-manager/internal/api"
	"backup-manager/internal/backup"
	"backup-manager/internal/notify"
	"backup-manager/internal/scheduler"
	"backup-manager/internal/store"
)

//go:embed web/*
var webFS embed.FS

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dbURL := requireEnv("DATABASE_URL")

	s, err := store.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("資料庫初始化失敗: %v", err)
	}
	defer s.Close()

	notifier := notify.NewSlack()

	runner := &backup.Runner{
		Store:    s,
		Notifier: notifier,
	}

	sched := scheduler.New(s, runner)
	if err := sched.Start(ctx); err != nil {
		log.Fatalf("排程器啟動失敗: %v", err)
	}
	defer sched.Stop()

	addr := getEnvOr("DASHBOARD_ADDR", ":8080")
	mux := http.NewServeMux()

	// 所有 API
	api.RegisterProjectRoutes(mux, s)
	api.RegisterTargetRoutes(mux, s)
	api.RegisterScheduleRoutes(mux, s, sched)
	api.RegisterRetentionRoutes(mux, s)
	api.RegisterRecordRoutes(mux, s)
	api.RegisterTriggerRoute(mux, s, runner)
	api.RegisterSummaryRoute(mux, s)
	api.RegisterAgentRoutes(mux, s)
	api.RegisterSyslogRoutes(mux, s)
	api.RegisterGcpRoutes(mux, s)
	api.RegisterIntegratedRoutes(mux, s, runner)
	mux.HandleFunc("GET /api/capabilities", api.HandleCapabilities)

	// 健康檢查
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 前端靜態檔案
	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("無法載入前端資源: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSub)))

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("[dashboard] 啟動於 %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[dashboard] HTTP server 錯誤: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("[dashboard] 正在關閉...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx) //nolint
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
