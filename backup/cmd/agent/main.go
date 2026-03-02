package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"backup-manager/internal/backup"
	"backup-manager/internal/client"
	"backup-manager/internal/notify"
	"backup-manager/internal/scheduler"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dashURL := requireEnv("DASHBOARD_URL") // e.g. http://127.0.0.1:8105
	agentToken := getEnvOr("AGENT_TOKEN", "")

	// 使用 HTTP client 取代直連 DB
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
