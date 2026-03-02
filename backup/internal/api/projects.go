package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"backup-manager/internal/store"
)

// projectHandler 持有 store
type projectHandler struct {
	store *store.Store
}

func RegisterProjectRoutes(mux *http.ServeMux, s *store.Store) {
	h := &projectHandler{store: s}

	mux.HandleFunc("GET /api/projects", h.list)
	mux.HandleFunc("POST /api/projects", h.create)
	mux.HandleFunc("GET /api/projects/{id}", h.get)
	mux.HandleFunc("PUT /api/projects/{id}", h.update)
	mux.HandleFunc("DELETE /api/projects/{id}", h.delete)
	mux.HandleFunc("PATCH /api/projects/{id}/toggle", h.toggle)
}

func (h *projectHandler) list(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (h *projectHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	proj, err := h.store.GetProject(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到專案")
		return
	}
	writeJSON(w, http.StatusOK, proj)
}

func (h *projectHandler) create(w http.ResponseWriter, r *http.Request) {
	var p store.Project
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	if p.NasBase == "" {
		p.NasBase = "/mnt/nas/backups"
	}
	p.Enabled = true
	result, err := h.store.CreateProject(r.Context(), &p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *projectHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var p store.Project
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	p.ID = id
	if err := h.store.UpdateProject(r.Context(), &p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *projectHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	if err := h.store.DeleteProject(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *projectHandler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
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
	if err := h.store.ToggleProject(r.Context(), id, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// pathID 從 {id} path value 解析整數
func pathID(r *http.Request, key string) (int, error) {
	return strconv.Atoi(r.PathValue(key))
}
