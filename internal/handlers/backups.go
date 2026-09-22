package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type BackupHandlers struct {
	Store      *store.Store
	BackupRoot string
}

func (h *BackupHandlers) ensureRoot() error {
	return os.MkdirAll(h.BackupRoot, 0o700)
}

func (h *BackupHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	backups := h.Store.ListBackupsForDeployment(dep.ID)
	middleware.JSON(w, http.StatusOK, backups)
}

type createBackupRequest struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
}

func (h *BackupHandlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req createBackupRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.ensureRoot(); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04:05")
	}
	if len(name) > 100 {
		middleware.JSONError(w, http.StatusBadRequest, "backup name too long")
		return
	}

	backupID := randomID()
	timestamp := time.Now().Format("20060102-150405")
	archiveName := fmt.Sprintf("%s-%s.tar.gz", dep.Name, timestamp)
	archivePath := filepath.Join(h.BackupRoot, archiveName)

	tarArgs := []string{
		"-czf", archivePath,
		"-C", filepath.Dir(dep.Path),
		"--exclude=node_modules",
		"--exclude=.git",
		"--exclude=vendor",
		"--exclude=__pycache__",
		filepath.Base(dep.Path),
	}
	out, err := runCommand(10*time.Minute, "tar", tarArgs...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "tar failed: "+out)
		return
	}

	imageTag := "vpscontrol-" + dep.Name
	backupImageTag := fmt.Sprintf("vpscontrol-backup-%s:%s", dep.Name, timestamp)
	if _, err := runCommand(2*time.Minute, "docker", "tag", imageTag, backupImageTag); err != nil {
		backupImageTag = ""
	}

	var size int64
	if info, err := os.Stat(archivePath); err == nil {
		size = info.Size()
	}

	b := store.Backup{
		ID:           backupID,
		DeploymentID: dep.ID,
		Name:         name,
		Filename:     archiveName,
		SizeBytes:    size,
		ImageTag:     backupImageTag,
		CreatedBy:    user.ID,
		CreatedAt:    time.Now(),
		Notes:        req.Notes,
	}
	if err := h.Store.AddBackup(b); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.JSON(w, http.StatusCreated, b)
}

func (h *BackupHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	backupID := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/backups/", dep.ID))
	if backupID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing backup id")
		return
	}

	b, ok := h.Store.FindBackupByID(backupID)
	if !ok || b.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "backup not found")
		return
	}

	archivePath := filepath.Join(h.BackupRoot, b.Filename)
	_ = os.Remove(archivePath)

	if b.ImageTag != "" {
		_, _ = runCommand(30*time.Second, "docker", "rmi", b.ImageTag)
	}

	if err := h.Store.DeleteBackup(backupID); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *BackupHandlers) Download(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/backups/", dep.ID))
	backupID := strings.TrimSuffix(trimmed, "/download")
	if backupID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing backup id")
		return
	}

	b, ok := h.Store.FindBackupByID(backupID)
	if !ok || b.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "backup not found")
		return
	}

	archivePath := filepath.Join(h.BackupRoot, b.Filename)
	if _, err := os.Stat(archivePath); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "backup file not found on disk")
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=\""+b.Filename+"\"")
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, archivePath)
}

func (h *BackupHandlers) Restore(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/backups/", dep.ID))
	backupID := strings.TrimSuffix(trimmed, "/restore")
	if backupID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing backup id")
		return
	}

	b, ok := h.Store.FindBackupByID(backupID)
	if !ok || b.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "backup not found")
		return
	}

	archivePath := filepath.Join(h.BackupRoot, b.Filename)
	if _, err := os.Stat(archivePath); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "backup file not found on disk")
		return
	}

	_, _ = runCommand(30*time.Second, "docker", "stop", dep.Container)

	safetyDir := dep.Path + ".pre-restore"
	_ = os.RemoveAll(safetyDir)
	_, _ = runCommand(2*time.Minute, "cp", "-a", dep.Path, safetyDir)

	_ = os.RemoveAll(dep.Path)
	_ = os.MkdirAll(dep.Path, 0o755)

	if out, err := runCommand(5*time.Minute, "tar",
		"-xzf", archivePath,
		"-C", filepath.Dir(dep.Path),
	); err != nil {
		_ = os.RemoveAll(dep.Path)
		_ = os.Rename(safetyDir, dep.Path)
		middleware.JSONError(w, http.StatusInternalServerError, "extract failed: "+out)
		return
	}

	_, _ = runCommand(30*time.Second, "docker", "start", dep.Container)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"message": "Restore complete. Container restarted.",
		"backup":  b.ID,
	})
}