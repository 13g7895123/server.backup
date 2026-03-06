package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── types ─────────────────────────────────────────────────────────────────────

// SSHEvent 代表一筆 SSH / sudo 稽核事件
type SSHEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"` // login | login_failed | logout | session_open | session_close | sudo | invalid_user | other
	User    string    `json:"user"`
	FromIP  string    `json:"from_ip,omitempty"`
	Port    string    `json:"port,omitempty"`
	Method  string    `json:"method,omitempty"`  // publickey | password
	Command string    `json:"command,omitempty"` // sudo 指令
	Raw     string    `json:"raw"`
}

// SSHAuditResponse 是 /api/ssh-audit 的回應結構
type SSHAuditResponse struct {
	Since     string     `json:"since"`
	Until     string     `json:"until"`
	Events    []SSHEvent `json:"events"`
	SumLogin  int        `json:"sum_login"`
	SumFailed int        `json:"sum_failed"`
	SumSudo   int        `json:"sum_sudo"`
	SumUsers  int        `json:"sum_unique_users"`
	SumIPs    int        `json:"sum_unique_ips"`
}

// journald JSON 格式的欄位（-o json 輸出）
type jEntry struct {
	Timestamp string      `json:"__REALTIME_TIMESTAMP"` // microseconds since epoch
	Message   interface{} `json:"MESSAGE"`              // 可能是 string 或 []int
	SyslogID  string      `json:"SYSLOG_IDENTIFIER"`
}

// ── regex ─────────────────────────────────────────────────────────────────────

var (
	reAccepted    = regexp.MustCompile(`Accepted (\w+) for (\S+) from (\S+) port (\d+)`)
	reFailed      = regexp.MustCompile(`Failed (\w+) for (?:invalid user )?(\S+) from (\S+) port (\d+)`)
	reInvalidUser = regexp.MustCompile(`Invalid user (\S+) from (\S+)(?:\s+port (\d+))?`)
	reDisconn     = regexp.MustCompile(`Disconnected from (?:authenticating |invalid )?user (\S+) (\S+) port (\d+)`)
	reSessOpen    = regexp.MustCompile(`session opened for user (\S+)`)
	reSessClose   = regexp.MustCompile(`session closed for user (\S+)`)
	reSudo        = regexp.MustCompile(`(\S+)\s*:\s*TTY=\S+\s*;\s*PWD=\S+\s*;\s*USER=\S+\s*;\s*COMMAND=(.+)`)
)

// ── handler ───────────────────────────────────────────────────────────────────

func RegisterSSHAuditRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ssh-audit", handleSSHAudit)
}

// HandleSSHAuditDirect 直接在本機跑 journalctl（供 agent 的 /ssh-audit 路由使用）
func HandleSSHAuditDirect(w http.ResponseWriter, r *http.Request) {
	sshAuditCore(w, r)
}

// handleSSHAudit 為 dashboard 用：若設定了 AGENT_URL 則 proxy 給 agent，否則本機執行
func handleSSHAudit(w http.ResponseWriter, r *http.Request) {
	if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
		proxySSHAuditToAgent(w, r, agentURL)
		return
	}
	sshAuditCore(w, r)
}

// proxySSHAuditToAgent 將請求轉發給 host agent 的 /ssh-audit
func proxySSHAuditToAgent(w http.ResponseWriter, r *http.Request, agentURL string) {
	target := agentURL + "/ssh-audit"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", target, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "建立 proxy 請求失敗: "+err.Error())
		return
	}
	if token := os.Getenv("AGENT_TOKEN"); token != "" {
		req.Header.Set("X-Agent-Token", token)
	}
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent 無回應: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

func sshAuditCore(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := q.Get("since")
	until := q.Get("until")
	filterUser := strings.TrimSpace(q.Get("user"))

	// 預設：今天 00:00 ~ 現在
	now := time.Now()
	today := now.Format("2006-01-02")
	if since == "" {
		since = today + " 00:00:00"
	}
	if until == "" {
		until = now.Format("2006-01-02 15:04:05")
	}

	events, err := CollectSSHEvents(since, until)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("journalctl 執行失敗: %v", err))
		return
	}

	// 過濾使用者
	if filterUser != "" {
		filtered := events[:0]
		for _, e := range events {
			if strings.EqualFold(e.User, filterUser) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	// 統計
	users := make(map[string]struct{})
	ips := make(map[string]struct{})
	var sumLogin, sumFailed, sumSudo int
	for _, e := range events {
		switch e.Type {
		case "login":
			sumLogin++
		case "login_failed", "invalid_user":
			sumFailed++
		case "sudo":
			sumSudo++
		}
		if e.User != "" {
			users[e.User] = struct{}{}
		}
		if e.FromIP != "" {
			ips[e.FromIP] = struct{}{}
		}
	}

	if events == nil {
		events = []SSHEvent{}
	}

	resp := SSHAuditResponse{
		Since:     since,
		Until:     until,
		Events:    events,
		SumLogin:  sumLogin,
		SumFailed: sumFailed,
		SumSudo:   sumSudo,
		SumUsers:  len(users),
		SumIPs:    len(ips),
	}
	writeJSON(w, http.StatusOK, resp)
}

// CollectSSHEvents 呼叫 journalctl 並解析所有 sshd / sudo 事件（導出供 agent 使用）
func CollectSSHEvents(since, until string) ([]SSHEvent, error) {
	// 同時讀取 sshd 和 sudo 的 journal
	args := []string{
		"--no-pager",
		"-o", "json",
		"--since", since,
		"--until", until,
		// journalctl 同欄位多值 = OR
		"SYSLOG_IDENTIFIER=sshd",
		"SYSLOG_IDENTIFIER=sudo",
	}

	cmd := exec.Command("journalctl", args...) //nolint:gosec
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("journalctl 無法啟動（是否安裝 systemd?）: %w", err)
	}

	var events []SSHEvent
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB per line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry jEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		msg := entryMessage(&entry)
		ts := parseTimestamp(entry.Timestamp)
		ev := parseSSHLine(ts, entry.SyslogID, msg)
		if ev != nil {
			events = append(events, *ev)
		}
	}
	cmd.Wait() //nolint:errcheck

	return events, nil
}

// entryMessage 取出 MESSAGE 欄位（可能是 string 或 []byte 的 int array）
func entryMessage(e *jEntry) string {
	switch v := e.Message.(type) {
	case string:
		return v
	case []interface{}:
		// journald 對 non-UTF8 訊息會用 byte array 表示
		b := make([]byte, len(v))
		for i, x := range v {
			if f, ok := x.(float64); ok {
				b[i] = byte(f)
			}
		}
		return string(b)
	}
	return ""
}

// parseTimestamp 將 journald 的微秒 Unix 時間戳轉為 time.Time
func parseTimestamp(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	us, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(us/1_000_000, (us%1_000_000)*1000).UTC()
}

// parseSSHLine 依照訊息內容分類事件，失敗回傳 nil（忽略）
func parseSSHLine(ts time.Time, syslogID, msg string) *SSHEvent {
	ev := &SSHEvent{Time: ts, Raw: msg}

	switch syslogID {
	case "sshd":
		if m := reAccepted.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.Method, ev.User, ev.FromIP, ev.Port = "login", m[1], m[2], m[3], m[4]
			return ev
		}
		if m := reFailed.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.Method, ev.User, ev.FromIP, ev.Port = "login_failed", m[1], m[2], m[3], m[4]
			return ev
		}
		if m := reInvalidUser.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.User, ev.FromIP, ev.Port = "invalid_user", m[1], m[2], m[3]
			return ev
		}
		if m := reDisconn.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.User, ev.FromIP, ev.Port = "logout", m[1], m[2], m[3]
			return ev
		}
		if m := reSessOpen.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.User = "session_open", m[1]
			return ev
		}
		if m := reSessClose.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.User = "session_close", m[1]
			return ev
		}
		// 其他 sshd 訊息也保留
		ev.Type = "other"
		return ev

	case "sudo":
		if m := reSudo.FindStringSubmatch(msg); m != nil {
			ev.Type, ev.User, ev.Command = "sudo", m[1], strings.TrimSpace(m[2])
			return ev
		}
		// sudo 認證失敗等也保留
		ev.Type = "sudo"
		return ev
	}

	return nil
}
