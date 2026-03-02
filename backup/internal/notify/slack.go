package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Slack 傳送備份結果通知
type Slack struct {
	WebhookURL string
}

// NewSlack 從環境變數 SLACK_WEBHOOK_URL 建立 Slack notifier
// 若未設定則回傳 nil（不傳通知）
func NewSlack() *Slack {
	url := os.Getenv("SLACK_WEBHOOK_URL")
	if url == "" {
		return nil
	}
	return &Slack{WebhookURL: url}
}

func (s *Slack) SendFailure(projectName, backupType, errMsg string) {
	text := fmt.Sprintf("❌ *備份失敗*\n專案: `%s`\n類型: `%s`\n時間: %s\n錯誤: ```%s```",
		projectName, backupType, time.Now().Format("2006-01-02 15:04:05"), errMsg)
	s.send(text)
}

func (s *Slack) SendSuccess(projectName, backupType, filename string, sizeMB float64) {
	text := fmt.Sprintf("✅ *備份成功*\n專案: `%s`\n類型: `%s`\n檔案: `%s`\n大小: %.2f MB\n時間: %s",
		projectName, backupType, filename, sizeMB, time.Now().Format("2006-01-02 15:04:05"))
	s.send(text)
}

func (s *Slack) send(text string) {
	body, _ := json.Marshal(map[string]string{"text": text})
	resp, err := http.Post(s.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[notify] Slack 通知失敗: %v\n", err)
		return
	}
	resp.Body.Close()
}
