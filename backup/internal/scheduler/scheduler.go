package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"backup-manager/internal/backup"
	"backup-manager/internal/store"
)

// ScheduleStore 是 scheduler 需要的底層儲存介面。
// store.Store 和 client.DashboardClient 都實作此介面。
type ScheduleStore interface {
	ListEnabledSchedules(ctx context.Context) ([]store.Schedule, error)
	GetSchedule(ctx context.Context, id int) (*store.Schedule, error)
	UpdateScheduleRunTime(ctx context.Context, id int, lastRun, nextRun time.Time) error
	UpdateScheduleStatus(ctx context.Context, id int, status string) error
}

// DynamicScheduler 動態管理 cron 排程，無需重啟即可更新
type DynamicScheduler struct {
	cron   *cron.Cron
	store  ScheduleStore
	runner *backup.Runner
	jobs   map[int]cron.EntryID // scheduleID → cron EntryID
	mu     sync.Mutex
}

func New(s ScheduleStore, r *backup.Runner) *DynamicScheduler {
	return &DynamicScheduler{
		cron:   cron.New(cron.WithLocation(time.Local)),
		store:  s,
		runner: r,
		jobs:   make(map[int]cron.EntryID),
	}
}

// Start 啟動排程器，從 DB 載入所有 enabled 排程
func (ds *DynamicScheduler) Start(ctx context.Context) error {
	schedules, err := ds.store.ListEnabledSchedules(ctx)
	if err != nil {
		return fmt.Errorf("載入排程失敗: %w", err)
	}
	for _, sch := range schedules {
		if err := ds.addJob(ctx, sch); err != nil {
			log.Printf("[scheduler] 無法載入排程 id=%d %q: %v", sch.ID, sch.CronExpr, err)
		}
	}
	ds.cron.Start()

	// 每天清理過期備份
	ds.cron.AddFunc("0 4 * * *", func() { //nolint
		ds.runner.DeleteExpiredBackups(ctx)
	})

	log.Printf("[scheduler] 已啟動，共載入 %d 個排程", len(schedules))
	return nil
}

func (ds *DynamicScheduler) Stop() {
	ds.cron.Stop()
}

// Reload 重新載入特定排程（從 API 新增/修改後呼叫）
func (ds *DynamicScheduler) Reload(ctx context.Context, scheduleID int) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// 先移除舊的
	if entryID, ok := ds.jobs[scheduleID]; ok {
		ds.cron.Remove(entryID)
		delete(ds.jobs, scheduleID)
	}

	sch, err := ds.store.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil // 排程可能已被刪除，忽略
	}
	if !sch.Enabled {
		return nil
	}
	return ds.addJob(ctx, *sch)
}

// Remove 移除排程（刪除時呼叫）
func (ds *DynamicScheduler) Remove(scheduleID int) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if entryID, ok := ds.jobs[scheduleID]; ok {
		ds.cron.Remove(entryID)
		delete(ds.jobs, scheduleID)
	}
}

// ActiveJobs 回傳目前執行中的排程 ID 列表
func (ds *DynamicScheduler) ActiveJobs() []int {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ids := make([]int, 0, len(ds.jobs))
	for id := range ds.jobs {
		ids = append(ids, id)
	}
	return ids
}

func (ds *DynamicScheduler) addJob(ctx context.Context, sch store.Schedule) error {
	schedID := sch.ID
	projID := sch.ProjectID
	targetTypes := sch.TargetTypes

	entryID, err := ds.cron.AddFunc(sch.CronExpr, func() {
		log.Printf("[scheduler] 觸發排程 id=%d project_id=%d", schedID, projID)

		now := time.Now()
		// 計算下一次執行時間
		next := ds.cron.Entry(ds.jobs[schedID]).Next

		_ = ds.store.UpdateScheduleRunTime(ctx, schedID, now, next)
		runErr := ds.runner.RunProject(ctx, projID, targetTypes, &schedID, "schedule")
		status := "success"
		if runErr != nil {
			status = "failed"
		}
		_ = ds.store.UpdateScheduleStatus(ctx, schedID, status)
	})
	if err != nil {
		return fmt.Errorf("無效 cron 表達式 %q: %w", sch.CronExpr, err)
	}

	ds.jobs[schedID] = entryID
	return nil
}
