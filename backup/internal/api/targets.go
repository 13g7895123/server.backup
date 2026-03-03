package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"backup-manager/internal/store"
)

type targetHandler struct{ store *store.Store }

func RegisterTargetRoutes(mux *http.ServeMux, s *store.Store) {
	h := &targetHandler{store: s}
	mux.HandleFunc("GET /api/projects/{id}/targets", h.list)
	mux.HandleFunc("GET /api/projects/{id}/targets/{tid}", h.get)
	mux.HandleFunc("POST /api/projects/{id}/targets", h.create)
	mux.HandleFunc("PUT /api/projects/{id}/targets/{tid}", h.update)
	mux.HandleFunc("DELETE /api/projects/{id}/targets/{tid}", h.delete)
}

func (h *targetHandler) get(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 target id")
		return
	}
	t, err := h.store.GetTarget(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到備份目標")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *targetHandler) list(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	targets, err := h.store.ListTargets(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (h *targetHandler) create(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	var t store.BackupTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if t.Type != "files" && t.Type != "database" && t.Type != "system" {
		writeError(w, http.StatusBadRequest, "type 必須為 files | database | system")
		return
	}
	t.ProjectID = projectID
	t.Enabled = true
	result, err := h.store.CreateTarget(r.Context(), &t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *targetHandler) update(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 target id")
		return
	}
	var t store.BackupTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	t.ID = tid
	if err := h.store.UpdateTarget(r.Context(), &t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *targetHandler) delete(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 target id")
		return
	}
	if err := h.store.DeleteTarget(r.Context(), tid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
