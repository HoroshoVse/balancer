package api

import (
	"net/http"
	"strconv"

	"github.com/balancer/backend/internal/accesslists"
	"github.com/balancer/backend/internal/models"
)

func (s *Server) handleAccessLists(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost, http.MethodPut) {
		return
	}
	if r.Method == http.MethodGet {
		lists, err := s.accessLists.List()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, lists)
		return
	}
	if !requireRole(w, r, models.RoleOperator) {
		return
	}
	var in accesslists.Input
	if !decodeJSONLimit(w, r, &in, 2*accesslists.MaxBytes+4096) {
		return
	}
	list, err := s.accessLists.Save(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if in.ID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, list)
}

func accessListID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 32)
	if err != nil || id == 0 {
		http.Error(w, "Укажите ID списка", http.StatusBadRequest)
		return 0, false
	}
	return uint(id), true
}

func (s *Server) refreshAccessList(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	id, ok := accessListID(w, r)
	if !ok {
		return
	}
	list, err := s.accessLists.Refresh(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) deleteAccessList(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost, http.MethodDelete) {
		return
	}
	id, ok := accessListID(w, r)
	if !ok {
		return
	}
	if err := s.accessLists.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
