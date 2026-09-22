package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type ScheduleHandlers struct {
	Store       *store.Store
	BackupRoot  string
	TriggerFunc func(action string) error
}

func validateCron(expr string) error {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return fmt.Errorf("cron expression must have exactly 5 fields (minute hour day month weekday)")
	}
	for _, p := range parts {
		if p == "" {
			return fmt.Errorf("empty cron field")
		}
	}
	return nil
}

func validateAction(action string) bool {
	switch action {
	case "backup", "restart", "command":
		return true
	}
	return false
}

func (h *ScheduleHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	schedules := h.Store.ListSchedulesForDeployment(dep.ID)
	middleware.JSON(w, http.StatusOK, schedules)
}

type scheduleRequest struct {
	Name     string `json:"name"`
	CronExpr string `json:"cronExpr"`
	Action   string `json:"action"`
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
}

func (h *ScheduleHandlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req scheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		middleware.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 100 {
		middleware.JSONError(w, http.StatusBadRequest, "name too long")
		return
	}
	if err := validateCron(req.CronExpr); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validateAction(req.Action) {
		middleware.JSONError(w, http.StatusBadRequest, "action must be one of: backup, restart, command")
		return
	}
	if req.Action == "command" && strings.TrimSpace(req.Command) == "" {
		middleware.JSONError(w, http.StatusBadRequest, "command is required when action=command")
		return
	}

	sc := store.Schedule{
		ID:           randomID(),
		DeploymentID: dep.ID,
		Name:         req.Name,
		CronExpr:     req.CronExpr,
		Action:       req.Action,
		Command:      req.Command,
		Enabled:      req.Enabled,
		CreatedBy:    user.ID,
		CreatedAt:    time.Now(),
	}
	if err := h.Store.AddSchedule(sc); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.TriggerFunc != nil {
		_ = h.TriggerFunc("reload")
	}

	middleware.JSON(w, http.StatusCreated, sc)
}

func (h *ScheduleHandlers) Update(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	schedID := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/schedules/", dep.ID))
	if schedID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing schedule id")
		return
	}

	sc, ok := h.Store.FindScheduleByID(schedID)
	if !ok || sc.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "schedule not found")
		return
	}

	var req scheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if req.Name != "" {
		sc.Name = strings.TrimSpace(req.Name)
	}
	if req.CronExpr != "" {
		if err := validateCron(req.CronExpr); err != nil {
			middleware.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		sc.CronExpr = req.CronExpr
	}
	if req.Action != "" {
		if !validateAction(req.Action) {
			middleware.JSONError(w, http.StatusBadRequest, "invalid action")
			return
		}
		sc.Action = req.Action
	}
	if req.Command != "" {
		sc.Command = req.Command
	}
	sc.Enabled = req.Enabled

	if err := h.Store.UpdateSchedule(sc); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.TriggerFunc != nil {
		_ = h.TriggerFunc("reload")
	}

	middleware.JSON(w, http.StatusOK, sc)
}

func (h *ScheduleHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	schedID := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/schedules/", dep.ID))
	if schedID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing schedule id")
		return
	}

	sc, ok := h.Store.FindScheduleByID(schedID)
	if !ok || sc.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err := h.Store.DeleteSchedule(schedID); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.TriggerFunc != nil {
		_ = h.TriggerFunc("reload")
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ScheduleHandlers) Run(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/schedules/", dep.ID))
	schedID := strings.TrimSuffix(trimmed, "/run")
	if schedID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing schedule id")
		return
	}

	sc, ok := h.Store.FindScheduleByID(schedID)
	if !ok || sc.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "schedule not found")
		return
	}

	if h.TriggerFunc != nil {
		if err := h.TriggerFunc(schedID); err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}