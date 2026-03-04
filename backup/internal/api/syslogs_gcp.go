package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"backup-manager/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── models ────────────────────────────────────────────────────────────────────

type SyslogConfig struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	LogType   string    `json:"log_type"`
	LogFiles  []string  `json:"log_files"`
	Dest      string    `json:"dest"`
	Compress  bool      `json:"compress"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GcpConfig struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	BackupDir    string    `json:"backup_dir"`
	BackupDbDir  string    `json:"backup_db_dir"`
	RemoteUser   string    `json:"remote_user"`
	RemoteHost   string    `json:"remote_host"`
	RemotePath   string    `json:"remote_path"`
	RemoteDbPath string    `json:"remote_db_path"`
	SshKey       string    `json:"ssh_key"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ── handler ───────────────────────────────────────────────────────────────────

type syslogHandler struct{ pool *pgxpool.Pool }
type gcpHandler struct{ pool *pgxpool.Pool }

func RegisterSyslogRoutes(mux *http.ServeMux, s *store.Store) {
	h := &syslogHandler{pool: s.Pool()}
	mux.HandleFunc("GET /api/syslogs", h.list)
	mux.HandleFunc("POST /api/syslogs", h.create)
	mux.HandleFunc("PUT /api/syslogs/{id}", h.update)
	mux.HandleFunc("DELETE /api/syslogs/{id}", h.delete)
	mux.HandleFunc("PATCH /api/syslogs/{id}/toggle", h.toggle)
}

func RegisterGcpRoutes(mux *http.ServeMux, s *store.Store) {
	h := &gcpHandler{pool: s.Pool()}
	mux.HandleFunc("GET /api/gcpconfigs", h.list)
	mux.HandleFunc("POST /api/gcpconfigs", h.create)
	mux.HandleFunc("PUT /api/gcpconfigs/{id}", h.update)
	mux.HandleFunc("DELETE /api/gcpconfigs/{id}", h.delete)
	mux.HandleFunc("PATCH /api/gcpconfigs/{id}/toggle", h.toggle)
}

// ── SyslogConfig CRUD ─────────────────────────────────────────────────────────

func (h *syslogHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, name, log_type, log_files, dest, compress, enabled, created_at, updated_at
		FROM syslog_configs ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var items []SyslogConfig
	for rows.Next() {
		var c SyslogConfig
		if err := rows.Scan(&c.ID, &c.Name, &c.LogType, &c.LogFiles, &c.Dest,
			&c.Compress, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, c)
	}
	if items == nil {
		items = []SyslogConfig{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *syslogHandler) create(w http.ResponseWriter, r *http.Request) {
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	if c.LogFiles == nil {
		c.LogFiles = []string{}
	}
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO syslog_configs (name, log_type, log_files, dest, compress, enabled)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, created_at, updated_at`,
		c.Name, c.LogType, c.LogFiles, c.Dest, c.Compress, c.Enabled).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *syslogHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.LogFiles == nil {
		c.LogFiles = []string{}
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE syslog_configs SET name=$1, log_type=$2, log_files=$3, dest=$4,
		  compress=$5, enabled=$6, updated_at=NOW()
		WHERE id=$7`,
		c.Name, c.LogType, c.LogFiles, c.Dest, c.Compress, c.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *syslogHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	_, err = h.pool.Exec(r.Context(), `DELETE FROM syslog_configs WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *syslogHandler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE syslog_configs SET enabled=$1, updated_at=NOW() WHERE id=$2`, body.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// ── GcpConfig CRUD ────────────────────────────────────────────────────────────

func (h *gcpHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, name, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled, created_at, updated_at
		FROM gcp_configs ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var items []GcpConfig
	for rows.Next() {
		var c GcpConfig
		if err := rows.Scan(&c.ID, &c.Name, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, c)
	}
	if items == nil {
		items = []GcpConfig{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *gcpHandler) create(w http.ResponseWriter, r *http.Request) {
	var c GcpConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO gcp_configs
		  (name, backup_dir, backup_db_dir, remote_user, remote_host,
		   remote_path, remote_db_path, ssh_key, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, created_at, updated_at`,
		c.Name, c.BackupDir, c.BackupDbDir, c.RemoteUser, c.RemoteHost,
		c.RemotePath, c.RemoteDbPath, c.SshKey, c.Enabled).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *gcpHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c GcpConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE gcp_configs SET
		  name=$1, backup_dir=$2, backup_db_dir=$3, remote_user=$4,
		  remote_host=$5, remote_path=$6, remote_db_path=$7, ssh_key=$8,
		  enabled=$9, updated_at=NOW()
		WHERE id=$10`,
		c.Name, c.BackupDir, c.BackupDbDir, c.RemoteUser,
		c.RemoteHost, c.RemotePath, c.RemoteDbPath, c.SshKey, c.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *gcpHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	_, err = h.pool.Exec(r.Context(), `DELETE FROM gcp_configs WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *gcpHandler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE gcp_configs SET enabled=$1, updated_at=NOW() WHERE id=$2`, body.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}
