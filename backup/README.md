# Backup Manager — OOP 架構說明

> Go 1.23 · PostgreSQL 16 · Docker Compose · systemd

---

## 目錄

1. [系統概覽](#系統概覽)
2. [OOP 設計原則](#oop-設計原則)
3. [領域模型 (Domain Models)](#領域模型-domain-models)
4. [介面契約 (Interfaces)](#介面契約-interfaces)
5. [核心類別](#核心類別)
6. [層次架構圖](#層次架構圖)
7. [元件相依關係](#元件相依關係)
8. [部署架構](#部署架構)
9. [環境變數](#環境變數)
10. [API 路由表](#api-路由表)

---

## 系統概覽

Backup Manager 是一個多專案備份管理平台，由兩個可執行檔組成：

| 元件 | 執行環境 | 職責 |
|---|---|---|
| `dashboard` | Docker container | HTTP API、前端 UI、排程控制、資料持久化 |
| `backup-agent` | Debian host（systemd） | 實際執行備份（檔案打包、DB dump）、寫入 NAS |

兩者之間透過 **HTTP REST API** 通訊。PostgreSQL 不對外暴露 port，所有資料庫讀寫由 dashboard 代理。

---

## OOP 設計原則

### 1. 單一職責原則 (SRP)

每個 package 只負責一件事：

```
internal/
  store/     → 資料庫 CRUD（只和 PostgreSQL 互動）
  backup/    → 備份執行邏輯（只管如何備份）
  scheduler/ → 排程管理（只管何時備份）
  client/    → HTTP client（只管如何呼叫 API）
  notify/    → 通知（只管如何發送 Slack）
  api/       → HTTP handler（只管 HTTP 請求/回應）
```

### 2. 開放封閉原則 (OCP)

新增備份類型（`database` / `files` / `system`）只需在 `internal/backup/` 新增一個函式，`Runner` 本身不修改。

### 3. 依賴反轉原則 (DIP) ← 核心設計

`Runner` 和 `DynamicScheduler` 都**依賴介面，而非具體實作**：

```
Runner.Store      → BackingStore interface
DynamicScheduler.store → ScheduleStore interface
```

同一個介面有兩個實作：
- `store.Store` — 直接連 PostgreSQL（用於 dashboard）
- `client.DashboardClient` — 透過 HTTP API（用於 host agent）

切換實作不需更改任何業務邏輯。

### 4. 介面隔離原則 (ISP)

不使用「萬能 Store 介面」，而是按使用方需求切割：

- `BackingStore`（12 個方法）— Runner 需要的最小集合
- `ScheduleStore`（3 個方法）— Scheduler 需要的最小集合

---

## 領域模型 (Domain Models)

定義於 `internal/store/models.go`，使用 Go struct 表達領域概念。

### Project（專案）

```
Project
├── ID, Name, Description, Enabled
├── NasBase          # 備份寫入的 NAS 根目錄
├── ProjectPath      # host 上的專案根路徑
├── BackupDirs[]     # 要備份的目錄清單
└── DB 連線資訊
    ├── DbType, DbHost, DbPort
    ├── DbName, DbUser, DbPasswordEnv
    └── DockerDbContainer  # 若使用 docker exec 備份
```

### BackupTarget（備份目標）

```
BackupTarget
├── ID, ProjectID
├── Type    # "database" | "files" | "system"
├── Label   # 顯示名稱
├── Config  # JSON，格式依 Type 而異
└── Enabled
```

Config 依 Type 反序列化為：

| Type | Config Struct | 說明 |
|---|---|---|
| `database` | `backup.DatabaseConfig` | host/port/user/password_env/container_name |
| `files` | `backup.FilesConfig` | source 目錄、compress、exclude |
| `system` | — | 執行 `backup.BackupSystem()` |

### Schedule（排程）

```
Schedule
├── ID, ProjectID, Label
├── CronExpr      # 標準 cron 表達式
├── TargetTypes[] # 此排程觸發哪些 type
├── Enabled
├── LastRunAt
└── NextRunAt
```

### RetentionPolicy（保留策略）

```
RetentionPolicy
├── ProjectID, TargetType
├── KeepDaily    # 保留幾天內每日最新一份
├── KeepWeekly   # 保留幾週內每週最新一份
└── KeepMonthly  # 保留幾個月內每月最新一份
```

### BackupRecord（備份紀錄）

```
BackupRecord
├── ID, ProjectID, TargetID, ScheduleID
├── Type, Label, Filename, Path
├── SizeBytes, SizeMB, Checksum
├── Status       # "running" | "success" | "failed"
├── ErrorMsg
├── TriggeredBy  # "scheduler" | "manual"
├── StartedAt, FinishedAt, RetainUntil
└── DeletedAt    # soft delete
```

---

## 介面契約 (Interfaces)

### BackingStore（備份引擎的資料存取介面）

定義於 `internal/backup/runner.go`：

```go
type BackingStore interface {
    GetProject(ctx, id)                           (*Project, error)
    ListTargets(ctx, projectID)                   ([]BackupTarget, error)
    ListRetention(ctx, projectID)                 ([]RetentionPolicy, error)
    CreateRecord(ctx, *BackupRecord)              (int64, error)
    UpdateRecord(ctx, *BackupRecord)              error
    ListRecords(ctx, ListRecordsFilter)           ([]BackupRecord, int64, error)
    DeleteRecord(ctx, id)                         (string, error)
}
```

### ScheduleStore（排程器的資料存取介面）

定義於 `internal/scheduler/scheduler.go`：

```go
type ScheduleStore interface {
    ListEnabledSchedules(ctx)                     ([]Schedule, error)
    GetSchedule(ctx, id)                          (*Schedule, error)
    UpdateScheduleRunTime(ctx, id, last, next)    error
}
```

### 介面實作對照表

| 介面 | `store.Store` | `client.DashboardClient` |
|---|---|---|
| `BackingStore` | ✅ PostgreSQL | ✅ HTTP API |
| `ScheduleStore` | ✅ PostgreSQL | ✅ HTTP API |

---

## 核心類別

### `store.Store` — 持久層

```
store.Store
├── 欄位：pool *pgxpool.Pool
├── New(ctx, databaseURL)   # 連線 + ping + migrate
├── Close()
│
├── Projects CRUD
├── BackupTargets CRUD
├── Schedules CRUD
├── RetentionPolicies CRUD
└── BackupRecords CRUD
    └── 實作 BackingStore + ScheduleStore 兩個介面
```

### `backup.Runner` — 備份執行引擎

```
backup.Runner
├── 欄位：
│   ├── Store    BackingStore   # 依賴介面（非具體型別）
│   └── Notifier *notify.Slack
│
└── RunTarget(ctx, project, target, scheduleID, triggeredBy)
    ├── 1. 建立 BackupRecord（status=running）
    ├── 2. 依 target.Type 分派：
    │   ├── "database" → backup.BackupDatabase()
    │   │   ├── 有 ContainerName → docker exec pg_dump/mysqldump
    │   │   └── 否則 → 直連 host DB
    │   ├── "files"    → backup.BackupFiles()
    │   └── "system"   → backup.BackupSystem()
    ├── 3. 計算 SHA-256、取得檔案大小
    ├── 4. 更新 BackupRecord（status=success/failed）
    ├── 5. 套用 RetentionPolicy（自動刪除舊備份）
    └── 6. 傳送 Slack 通知（成功/失敗）
```

### `scheduler.DynamicScheduler` — 動態排程器

```
DynamicScheduler
├── 欄位：
│   ├── cron   *cron.Cron
│   ├── store  ScheduleStore   # 依賴介面
│   ├── runner *backup.Runner
│   └── jobs   map[int]cron.EntryID
│
├── Start(ctx)          # 從 DB 載入所有 enabled 排程
├── Stop()
├── Reload(ctx)         # 清除並重新載入所有排程（CRUD 後呼叫）
├── AddSchedule(ctx, s) # 動態新增
├── RemoveSchedule(id)  # 動態移除
└── addJob(ctx, s)      # 內部：向 cron 登記 func
```

### `client.DashboardClient` — HTTP 介面卡

```
DashboardClient
├── 欄位：
│   ├── base  string       # http://127.0.0.1:8105
│   ├── token string       # X-Agent-Token 驗證
│   └── http  *http.Client
│
├── 實作 BackingStore（7 個方法）
│   ├── GetProject         → GET  /api/projects/{id}
│   ├── ListTargets        → GET  /api/projects/{id}/targets
│   ├── ListRetention      → GET  /api/projects/{id}/retention
│   ├── CreateRecord       → POST /api/agent/records
│   ├── UpdateRecord       → PUT  /api/agent/records/{id}
│   ├── ListRecords        → GET  /api/backups
│   └── DeleteRecord       → DELETE /api/backups/{id}
│
└── 實作 ScheduleStore（3 個方法）
    ├── ListEnabledSchedules  → GET  /api/agent/schedules/enabled
    ├── GetSchedule           → GET  /api/agent/schedules/{id}
    └── UpdateScheduleRunTime → POST /api/agent/schedules/{id}/runtime
```

### `notify.Slack` — 通知器

```
notify.Slack
├── 欄位：WebhookURL string
├── NewSlack()          # 讀環境變數 SLACK_WEBHOOK_URL；若未設定回傳 nil
├── SendSuccess(project, type, filename, sizeMB)
└── SendFailure(project, type, errMsg)
```

---

## 層次架構圖

```
┌─────────────────────────────────────────────────────────┐
│                   HTTP Layer (api/)                     │
│  projects.go  targets.go  schedules.go  records.go      │
│  retention.go  trigger.go  agent.go  summary.go         │
└────────────────────┬────────────────────────────────────┘
                     │ uses
┌────────────────────▼────────────────────────────────────┐
│              Service / Engine Layer                     │
│                                                         │
│  ┌──────────────────┐   ┌──────────────────────────┐   │
│  │  backup.Runner   │   │ scheduler.DynamicScheduler│   │
│  │  (執行備份)       │   │  (管理 cron 排程)         │   │
│  └────────┬─────────┘   └────────────┬─────────────┘   │
└───────────┼──────────────────────────┼─────────────────┘
            │ BackingStore interface   │ ScheduleStore interface
            │ (依賴倒置)               │ (依賴倒置)
            ▼                         ▼
 ┌──────────────────────────────────────────────────────┐
 │          Storage Abstraction (兩種實作)              │
 │                                                      │
 │  ┌────────────────┐    ┌──────────────────────────┐  │
 │  │  store.Store   │    │  client.DashboardClient  │  │
 │  │ (直連 Postgres) │    │  (HTTP  API 介面卡)       │  │
 │  └───────┬────────┘    └──────────────────────────┘  │
 └──────────┼───────────────────────────────────────────┘
            │
 ┌──────────▼───────────┐
 │  PostgreSQL 16        │
 │  (Docker, 不對外暴露) │
 └──────────────────────┘
```

---

## 元件相依關係

```
cmd/dashboard/main.go
    ├── store.New()                     # 建立持久層
    ├── backup.Runner{Store: store}     # 注入 store（實作 BackingStore）
    ├── scheduler.New(store, runner)    # 注入 store（實作 ScheduleStore）
    └── api.Register*Routes(mux, store) # 注入 store

cmd/agent/main.go
    ├── client.New(dashURL, token)      # 建立 HTTP 介面卡
    ├── backup.Runner{Store: client}    # 注入 client（同樣實作 BackingStore）
    └── scheduler.New(client, runner)   # 注入 client（同樣實作 ScheduleStore）
```

`Runner` 和 `Scheduler` 完全不知道自己使用的是「PostgreSQL 直連」還是「HTTP 轉發」，這正是依賴倒置的效果。

---

## 部署架構

```
┌─────────────────────── Debian Host ──────────────────────────────┐
│                                                                   │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │  Docker Network (backup_default)                         │    │
│  │                                                          │    │
│  │  ┌────────────────────┐  ┌──────────────────────────┐   │    │
│  │  │   backup-dashboard │  │    backup-postgres        │   │    │
│  │  │   :8080 (container)│  │    :5432 (container only) │   │    │
│  │  │                    │──│    無對外 port mapping     │   │    │
│  │  │  store.Store       │  └──────────────────────────┘   │    │
│  │  └────────────────────┘                                  │    │
│  └──────────────────────────────────────────────────────────┘    │
│                    ▲ HTTP :8105                                   │
│                    │                                             │
│  ┌─────────────────┴──────────────────────────────────────────┐  │
│  │  backup-agent  (systemd, root)                             │  │
│  │  client.DashboardClient → POST /api/agent/*                │  │
│  │                                                            │  │
│  │  ├── docker exec pg_dump  (其他 container 的 DB)           │  │
│  │  ├── tar.gz 檔案備份                                        │  │
│  │  └── 寫入 /mnt/nas/backups/                                │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘
```

---

## 環境變數

### dashboard（docker-compose）

| 變數 | 必填 | 說明 |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL 連線字串（由 compose 自動組合） |
| `DASHBOARD_PORT` | ✅ | 對外 HTTP port（預設 8105） |
| `AGENT_TOKEN` | — | 若設定，agent 呼叫 `/api/agent/*` 須帶此值 |
| `SLACK_WEBHOOK_URL` | — | Slack 備份通知 Webhook |

### backup-agent（/etc/backup-agent/env）

| 變數 | 必填 | 說明 |
|---|---|---|
| `DASHBOARD_URL` | ✅ | dashboard 的基底 URL，例如 `http://127.0.0.1:8105` |
| `AGENT_TOKEN` | — | 與 dashboard 端對應的 shared secret |
| `HOST_PREFIX` | — | Host 路徑前綴（容器掛載用，通常留空） |
| `NAS_BASE` | — | 備份寫入根目錄（預設 `/mnt/nas/backups`） |
| `SLACK_WEBHOOK_URL` | — | Slack 通知（agent 側） |

---

## API 路由表

### 公開 API（前端 / 管理工具使用）

| 方法 | 路徑 | 說明 |
|---|---|---|
| GET | `/api/projects` | 列出所有專案 |
| POST | `/api/projects` | 建立專案 |
| GET | `/api/projects/{id}` | 取得單一專案 |
| PUT | `/api/projects/{id}` | 更新專案 |
| DELETE | `/api/projects/{id}` | 刪除專案 |
| GET/POST | `/api/projects/{id}/targets` | 備份目標 |
| GET/POST | `/api/projects/{id}/schedules` | 排程 |
| GET/POST | `/api/projects/{id}/retention` | 保留策略 |
| GET | `/api/backups` | 列出備份紀錄 |
| DELETE | `/api/backups/{id}` | 刪除備份紀錄 |
| POST | `/api/trigger` | 手動觸發備份 |
| GET | `/api/summary` | 統計摘要 |
| GET | `/api/capabilities` | 系統能力資訊 |
| GET | `/healthz` | 健康檢查 |

### Agent 專用 API（`X-Agent-Token` 驗證）

| 方法 | 路徑 | 說明 |
|---|---|---|
| GET | `/api/agent/schedules/enabled` | 取得所有啟用排程 |
| GET | `/api/agent/schedules/{id}` | 取得單一排程 |
| POST | `/api/agent/schedules/{id}/runtime` | 更新排程執行時間 |
| POST | `/api/agent/records` | 建立備份紀錄 |
| PUT | `/api/agent/records/{id}` | 更新備份紀錄 |

---

## 快速開始

```bash
# 1. 複製環境變數設定
cp .env.example .env
vi .env   # 填入 PG_PASSWORD、AGENT_TOKEN（選填）

# 2. 啟動 dashboard + postgres
docker compose up -d --build

# 3. 編譯 host agent
bash scripts/build-agent.sh

# 4. 安裝 systemd service（需 root）
sudo DASHBOARD_URL=http://127.0.0.1:8105 bash scripts/install-agent.sh

# 5. 確認狀態
systemctl status backup-agent
docker compose ps
```
