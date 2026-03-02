package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Project 對應 projects 表
type Project struct {
	ID                int       `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Enabled           bool      `json:"enabled"`
	NasBase           string    `json:"nas_base"`
	ProjectPath       string    `json:"project_path"`
	BackupDirs        []string  `json:"backup_dirs"`
	DbType            string    `json:"db_type"`
	DbHost            string    `json:"db_host"`
	DbPort            int       `json:"db_port"`
	DbName            string    `json:"db_name"`
	DbUser            string    `json:"db_user"`
	DbPasswordEnv     string    `json:"db_password_env"`
	DockerDbContainer string    `json:"docker_db_container"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// BackupTarget 對應 backup_targets 表
type BackupTarget struct {
	ID        int             `json:"id"`
	ProjectID int             `json:"project_id"`
	Type      string          `json:"type"`
	Label     string          `json:"label"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Schedule 對應 schedules 表
type Schedule struct {
	ID          int        `json:"id"`
	ProjectID   int        `json:"project_id"`
	Label       string     `json:"label"`
	CronExpr    string     `json:"cron_expr"`
	TargetTypes []string   `json:"target_types"`
	Enabled     bool       `json:"enabled"`
	LastRunAt   *time.Time `json:"last_run_at"`
	NextRunAt   *time.Time `json:"next_run_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// RetentionPolicy 對應 retention_policies 表
type RetentionPolicy struct {
	ID          int    `json:"id"`
	ProjectID   int    `json:"project_id"`
	TargetType  string `json:"target_type"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
}

// BackupRecord 對應 backup_records 表
type BackupRecord struct {
	ID            int64      `json:"id"`
	ProjectID     *int       `json:"project_id"`
	ProjectName   string     `json:"project_name"`
	TargetID      *int       `json:"target_id"`
	ScheduleID    *int       `json:"schedule_id"`
	Type          string     `json:"type"`
	SubType       string     `json:"sub_type"`
	Label         string     `json:"label"`
	Filename      string     `json:"filename"`
	Path          string     `json:"path"`
	SizeBytes     int64      `json:"size_bytes"`
	SizeMB        float64    `json:"size_mb"`
	Checksum      string     `json:"checksum"`
	Status        string     `json:"status"`
	DurationSec   float64    `json:"duration_sec"`
	ErrorMsg      string     `json:"error_msg"`
	TriggeredBy   string     `json:"triggered_by"`
	RetainedUntil *time.Time `json:"retained_until"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ── Projects ──────────────────────────────────────────────────────────────────

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, enabled, nas_base,
		       project_path, backup_dirs, db_type, db_host, db_port,
		       db_name, db_user, db_password_env, docker_db_container,
		       created_at, updated_at
		FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Enabled,
			&p.NasBase, &p.ProjectPath, &p.BackupDirs, &p.DbType, &p.DbHost,
			&p.DbPort, &p.DbName, &p.DbUser, &p.DbPasswordEnv,
			&p.DockerDbContainer, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

func (s *Store) GetProject(ctx context.Context, id int) (*Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, description, enabled, nas_base,
		       project_path, backup_dirs, db_type, db_host, db_port,
		       db_name, db_user, db_password_env, docker_db_container,
		       created_at, updated_at
		FROM projects WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.Enabled, &p.NasBase,
			&p.ProjectPath, &p.BackupDirs, &p.DbType, &p.DbHost, &p.DbPort,
			&p.DbName, &p.DbUser, &p.DbPasswordEnv, &p.DockerDbContainer,
			&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) CreateProject(ctx context.Context, p *Project) (*Project, error) {
	if p.BackupDirs == nil {
		p.BackupDirs = []string{}
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects
		  (name, description, enabled, nas_base,
		   project_path, backup_dirs, db_type, db_host, db_port,
		   db_name, db_user, db_password_env, docker_db_container)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, created_at, updated_at`,
		p.Name, p.Description, p.Enabled, p.NasBase,
		p.ProjectPath, p.BackupDirs, p.DbType, p.DbHost, p.DbPort,
		p.DbName, p.DbUser, p.DbPasswordEnv, p.DockerDbContainer).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	if p.BackupDirs == nil {
		p.BackupDirs = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE projects SET
		  name=$1, description=$2, enabled=$3, nas_base=$4,
		  project_path=$5, backup_dirs=$6, db_type=$7, db_host=$8, db_port=$9,
		  db_name=$10, db_user=$11, db_password_env=$12, docker_db_container=$13,
		  updated_at=NOW()
		WHERE id=$14`,
		p.Name, p.Description, p.Enabled, p.NasBase,
		p.ProjectPath, p.BackupDirs, p.DbType, p.DbHost, p.DbPort,
		p.DbName, p.DbUser, p.DbPasswordEnv, p.DockerDbContainer, p.ID)
	return err
}

func (s *Store) DeleteProject(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	return err
}

func (s *Store) ToggleProject(ctx context.Context, id int, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE projects SET enabled=$1, updated_at=NOW() WHERE id=$2`, enabled, id)
	return err
}

// ── BackupTargets ─────────────────────────────────────────────────────────────

func (s *Store) ListTargets(ctx context.Context, projectID int) ([]BackupTarget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, type, label, config, enabled, created_at, updated_at
		FROM backup_targets WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []BackupTarget
	for rows.Next() {
		var t BackupTarget
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Type, &t.Label,
			&t.Config, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func (s *Store) GetTarget(ctx context.Context, id int) (*BackupTarget, error) {
	var t BackupTarget
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, type, label, config, enabled, created_at, updated_at
		FROM backup_targets WHERE id=$1`, id).
		Scan(&t.ID, &t.ProjectID, &t.Type, &t.Label,
			&t.Config, &t.Enabled, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) CreateTarget(ctx context.Context, t *BackupTarget) (*BackupTarget, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO backup_targets (project_id, type, label, config, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`,
		t.ProjectID, t.Type, t.Label, t.Config, t.Enabled).
		Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) UpdateTarget(ctx context.Context, t *BackupTarget) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_targets SET type=$1, label=$2, config=$3, enabled=$4, updated_at=NOW()
		WHERE id=$5`,
		t.Type, t.Label, t.Config, t.Enabled, t.ID)
	return err
}

func (s *Store) DeleteTarget(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM backup_targets WHERE id=$1`, id)
	return err
}

// ── Schedules ─────────────────────────────────────────────────────────────────

func (s *Store) ListSchedules(ctx context.Context, projectID int) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, label, cron_expr, target_types, enabled,
		       last_run_at, next_run_at, created_at, updated_at
		FROM schedules WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSchedules(rows)
}

func (s *Store) ListEnabledSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.project_id, s.label, s.cron_expr, s.target_types, s.enabled,
		       s.last_run_at, s.next_run_at, s.created_at, s.updated_at
		FROM schedules s
		JOIN projects p ON p.id = s.project_id
		WHERE s.enabled = true AND p.enabled = true
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSchedules(rows)
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
}

func scanSchedules(rows pgxRows) ([]Schedule, error) {
	var schedules []Schedule
	for rows.Next() {
		var sch Schedule
		if err := rows.Scan(&sch.ID, &sch.ProjectID, &sch.Label, &sch.CronExpr,
			&sch.TargetTypes, &sch.Enabled, &sch.LastRunAt, &sch.NextRunAt,
			&sch.CreatedAt, &sch.UpdatedAt); err != nil {
			return nil, err
		}
		schedules = append(schedules, sch)
	}
	return schedules, nil
}

func (s *Store) GetSchedule(ctx context.Context, id int) (*Schedule, error) {
	var sch Schedule
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, label, cron_expr, target_types, enabled,
		       last_run_at, next_run_at, created_at, updated_at
		FROM schedules WHERE id=$1`, id).
		Scan(&sch.ID, &sch.ProjectID, &sch.Label, &sch.CronExpr,
			&sch.TargetTypes, &sch.Enabled, &sch.LastRunAt, &sch.NextRunAt,
			&sch.CreatedAt, &sch.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &sch, nil
}

func (s *Store) CreateSchedule(ctx context.Context, sch *Schedule) (*Schedule, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO schedules (project_id, label, cron_expr, target_types, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`,
		sch.ProjectID, sch.Label, sch.CronExpr, sch.TargetTypes, sch.Enabled).
		Scan(&sch.ID, &sch.CreatedAt, &sch.UpdatedAt)
	return sch, err
}

func (s *Store) UpdateSchedule(ctx context.Context, sch *Schedule) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE schedules SET label=$1, cron_expr=$2, target_types=$3, enabled=$4, updated_at=NOW()
		WHERE id=$5`,
		sch.Label, sch.CronExpr, sch.TargetTypes, sch.Enabled, sch.ID)
	return err
}

func (s *Store) DeleteSchedule(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM schedules WHERE id=$1`, id)
	return err
}

func (s *Store) ToggleSchedule(ctx context.Context, id int, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE schedules SET enabled=$1, updated_at=NOW() WHERE id=$2`, enabled, id)
	return err
}

func (s *Store) UpdateScheduleRunTime(ctx context.Context, id int, lastRun, nextRun time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE schedules SET last_run_at=$1, next_run_at=$2 WHERE id=$3`,
		lastRun, nextRun, id)
	return err
}

// ── RetentionPolicies ─────────────────────────────────────────────────────────

func (s *Store) ListRetention(ctx context.Context, projectID int) ([]RetentionPolicy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, target_type, keep_daily, keep_weekly, keep_monthly
		FROM retention_policies WHERE project_id=$1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []RetentionPolicy
	for rows.Next() {
		var rp RetentionPolicy
		if err := rows.Scan(&rp.ID, &rp.ProjectID, &rp.TargetType,
			&rp.KeepDaily, &rp.KeepWeekly, &rp.KeepMonthly); err != nil {
			return nil, err
		}
		policies = append(policies, rp)
	}
	return policies, nil
}

func (s *Store) UpsertRetention(ctx context.Context, rp *RetentionPolicy) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO retention_policies (project_id, target_type, keep_daily, keep_weekly, keep_monthly)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, target_type)
		DO UPDATE SET keep_daily=$3, keep_weekly=$4, keep_monthly=$5`,
		rp.ProjectID, rp.TargetType, rp.KeepDaily, rp.KeepWeekly, rp.KeepMonthly)
	return err
}

// ── BackupRecords ─────────────────────────────────────────────────────────────

type ListRecordsFilter struct {
	ProjectID *int
	Type      string
	Status    string
	Limit     int
	Offset    int
}

func (s *Store) ListRecords(ctx context.Context, f ListRecordsFilter) ([]BackupRecord, int64, error) {
	where := "WHERE 1=1"
	args := []any{}
	i := 1

	if f.ProjectID != nil {
		where += fmt.Sprintf(" AND project_id=$%d", i)
		args = append(args, *f.ProjectID)
		i++
	}
	if f.Type != "" {
		where += fmt.Sprintf(" AND type=$%d", i)
		args = append(args, f.Type)
		i++
	}
	if f.Status != "" {
		where += fmt.Sprintf(" AND status=$%d", i)
		args = append(args, f.Status)
		i++
	}

	var total int64
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM backup_records "+where, args...).Scan(&total) //nolint

	if f.Limit == 0 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, project_id, project_name, target_id, schedule_id, type, sub_type,
		       label, filename, path, size_bytes,
		       ROUND(size_bytes::numeric/1024/1024, 2),
		       COALESCE(checksum,''), status, COALESCE(duration_sec,0),
		       COALESCE(error_msg,''), triggered_by, retained_until, created_at
		FROM backup_records %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, i, i+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var records []BackupRecord
	for rows.Next() {
		var r BackupRecord
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ProjectName, &r.TargetID,
			&r.ScheduleID, &r.Type, &r.SubType, &r.Label,
			&r.Filename, &r.Path, &r.SizeBytes, &r.SizeMB,
			&r.Checksum, &r.Status, &r.DurationSec, &r.ErrorMsg,
			&r.TriggeredBy, &r.RetainedUntil, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		records = append(records, r)
	}
	return records, total, nil
}

func (s *Store) CreateRecord(ctx context.Context, r *BackupRecord) (int64, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO backup_records
		  (project_id, project_name, target_id, schedule_id, type, sub_type,
		   label, filename, path, triggered_by, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'running')
		RETURNING id`,
		r.ProjectID, r.ProjectName, r.TargetID, r.ScheduleID, r.Type, r.SubType,
		r.Label, r.Filename, r.Path, r.TriggeredBy).Scan(&r.ID)
	return r.ID, err
}

func (s *Store) UpdateRecord(ctx context.Context, r *BackupRecord) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_records
		SET status=$1, size_bytes=$2, checksum=$3, duration_sec=$4,
		    error_msg=$5, retained_until=$6
		WHERE id=$7`,
		r.Status, r.SizeBytes, r.Checksum, r.DurationSec,
		r.ErrorMsg, r.RetainedUntil, r.ID)
	return err
}

func (s *Store) DeleteRecord(ctx context.Context, id int64) (string, error) {
	var path string
	err := s.pool.QueryRow(ctx,
		`DELETE FROM backup_records WHERE id=$1 RETURNING path`, id).Scan(&path)
	return path, err
}

// Summary 統計摘要
type ProjectSummary struct {
	ProjectID    int        `json:"project_id"`
	ProjectName  string     `json:"project_name"`
	TotalCount   int64      `json:"total_count"`
	SuccessCount int64      `json:"success_count"`
	FailedCount  int64      `json:"failed_count"`
	TotalSizeGB  float64    `json:"total_size_gb"`
	LastBackupAt *time.Time `json:"last_backup_at"`
	LastStatus   string     `json:"last_status"`
}

func (s *Store) Summary(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
		  project_id,
		  project_name,
		  COUNT(*) AS total_count,
		  SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),
		  SUM(CASE WHEN status='failed'  THEN 1 ELSE 0 END),
		  ROUND(SUM(size_bytes)::numeric / 1024 / 1024 / 1024, 3),
		  MAX(created_at),
		  (SELECT status FROM backup_records r2
		   WHERE r2.project_id = r.project_id ORDER BY created_at DESC LIMIT 1)
		FROM backup_records r
		GROUP BY project_id, project_name
		ORDER BY project_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []ProjectSummary
	for rows.Next() {
		var ps ProjectSummary
		if err := rows.Scan(&ps.ProjectID, &ps.ProjectName, &ps.TotalCount,
			&ps.SuccessCount, &ps.FailedCount, &ps.TotalSizeGB,
			&ps.LastBackupAt, &ps.LastStatus); err != nil {
			return nil, err
		}
		summaries = append(summaries, ps)
	}
	return summaries, nil
}
