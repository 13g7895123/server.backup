package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"backup-manager/internal/store"
)

type retentionHandler struct{ store *store.Store }

func RegisterRetentionRoutes(mux *http.ServeMux, s *store.Store) {
	h := &retentionHandler{store: s}
	mux.HandleFunc("GET /api/projects/{id}/retention", h.get)
	mux.HandleFunc("PUT /api/projects/{id}/retention", h.upsert)
}

func (h *retentionHandler) get(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	policies, err := h.store.ListRetention(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, policies)
}

func (h *retentionHandler) upsert(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	var policies []store.RetentionPolicy
	if err := json.NewDecoder(r.Body).Decode(&policies); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	for _, p := range policies {
		p.ProjectID = projectID
		if err := h.store.UpsertRetention(r.Context(), &p); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── Backup Records ────────────────────────────────────────────────────────────

type recordHandler struct{ store *store.Store }

func RegisterRecordRoutes(mux *http.ServeMux, s *store.Store) {
	h := &recordHandler{store: s}
	mux.HandleFunc("GET /api/backups", h.list)
	mux.HandleFunc("GET /api/projects/{id}/backups", h.listByProject)
	mux.HandleFunc("DELETE /api/backups/{bid}", h.delete)
}

func (h *recordHandler) list(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	records, total, err := h.store.ListRecords(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
		"limit":   f.Limit,
		"offset":  f.Offset,
	})
}

func (h *recordHandler) listByProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	f := parseFilter(r)
	f.ProjectID = &projectID
	records, total, err := h.store.ListRecords(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
		"limit":   f.Limit,
		"offset":  f.Offset,
	})
}

func (h *recordHandler) delete(w http.ResponseWriter, r *http.Request) {
	bid, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 backup id")
		return
	}
	path, err := h.store.DeleteRecord(r.Context(), bid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
		"path":   path,
	})
}

func parseFilter(r *http.Request) store.ListRecordsFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit == 0 {
		limit = 50
	}
	return store.ListRecordsFilter{
		Type:   q.Get("type"),
		Status: q.Get("status"),
		Limit:  limit,
		Offset: offset,
	}
}

// ── Summary ───────────────────────────────────────────────────────────────────

func RegisterSummaryRoute(mux *http.ServeMux, s *store.Store) {
	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, r *http.Request) {
		summaries, err := s.Summary(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": summaries})
	})
}
