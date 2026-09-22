package handlers

import (
	"net/http"
	"strconv"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type ActivityHandlers struct {
	Store *store.Store
}

func (h *ActivityHandlers) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	entries := h.Store.ListActivities(limit)
	middleware.JSON(w, http.StatusOK, entries)
}