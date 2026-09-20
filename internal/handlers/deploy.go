package handlers

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type DeployHandlers struct {
	Store      *store.Store
	DeployRoot string // root folder apps get cloned/extracted into, e.g. /opt/vpscontrol/apps
}

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

// Dockerfiles generated automatically when the repo doesn't already ship one.
// Deliberately simple: a single container per app, to stay lightweight and
// easy to understand/tweak on a 2GB VPS.
const dockerfileLaravel = `FROM richarvey/nginx-php-fpm:3.1.6
COPY . /var/www/html
ENV WEBROOT /var/www/html/public
ENV PHP_ERRORS_STDERR 1
ENV RUN_SCRIPTS 1
ENV REAL_IP_HEADER 1
ENV COMPOSER_ALLOW_SUPERUSER 1
RUN chmod -R 755 /var/www/html/storage /var/www/html/bootstrap/cache || true
CMD ["/start.sh"]
`

const dockerfileNode = `FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev || npm install --production
COPY . .
ENV PORT=%s
EXPOSE %s
CMD ["npm", "start"]
`

const dockerfilePython = `FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
COPY . .
ENV PORT=%s
EXPOSE %s
CMD ["python", "app.py"]
`

const dockerfileStatic = `FROM nginx:alpine
COPY . /usr/share/nginx/html
`

func detectStack(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case has("composer.json"):
		return "laravel"
	case has("package.json"):
		return "node"
	case has("requirements.txt") || has("app.py") || has("manage.py"):
		return "python"
	default:
		return "static"
	}
}

func writeDockerfileIfMissing(dir, stack, port string) error {
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err == nil {
		return nil // the repo already ships its own Dockerfile, respect it
	}
	var content string
	switch stack {
	case "laravel":
		content = dockerfileLaravel
	case "node":
		content = fmt.Sprintf(dockerfileNode, port, port)
	case "python":
		content = fmt.Sprintf(dockerfilePython, port, port)
	default:
		content = dockerfileStatic
	}
	return os.WriteFile(dockerfilePath, []byte(content), 0o644)
}

type deployGitRequest struct {
	Name    string `json:"name"`
	RepoURL string `json:"repoUrl"`
	Stack   string `json:"stack"` // "auto" or a specific stack
	Port    string `json:"port"`  // port exposed on the host
}

func containerPortForStack(stack string) string {
	switch stack {
	case "laravel":
		return "80"
	case "node":
		return "3000"
	case "python":
		return "5000"
	default:
		return "80"
	}
}

func (h *DeployHandlers) DeployGit(w http.ResponseWriter, r *http.Request) {
	var req deployGitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !appNameRe.MatchString(req.Name) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid name (lowercase letters, digits, dashes, 2-40 characters)")
		return
	}
	if req.RepoURL == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing repository URL")
		return
	}
	if _, err := strconv.Atoi(req.Port); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid port")
		return
	}
	targetDir := filepath.Join(h.DeployRoot, req.Name)
	if _, err := os.Stat(targetDir); err == nil {
		middleware.JSONError(w, http.StatusConflict, "a folder already exists for this name, pick another one")
		return
	}
	if err := os.MkdirAll(h.DeployRoot, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := runCommand(2*time.Minute, "git", "clone", "--depth", "1", req.RepoURL, targetDir)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "git clone failed: "+out)
		return
	}
	h.buildAndRun(w, req.Name, targetDir, req.Stack, req.Port, "git", req.RepoURL)
}

func (h *DeployHandlers) DeployUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "upload too large or invalid")
		return
	}
	name := r.FormValue("name")
	stack := r.FormValue("stack")
	port := r.FormValue("port")
	if !appNameRe.MatchString(name) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid name (lowercase letters, digits, dashes, 2-40 characters)")
		return
	}
	if _, err := strconv.Atoi(port); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid port")
		return
	}
	file, header, err := r.FormFile("archive")
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "missing .zip file (field 'archive')")
		return
	}
	defer file.Close()

	targetDir := filepath.Join(h.DeployRoot, name)
	if _, err := os.Stat(targetDir); err == nil {
		middleware.JSONError(w, http.StatusConflict, "a folder already exists for this name, pick another one")
		return
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpZip := filepath.Join(h.DeployRoot, name+".upload.zip")
	dst, err := os.Create(tmpZip)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dst.Close()
	defer os.Remove(tmpZip)

	if err := unzip(tmpZip, targetDir); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "failed to unzip: "+err.Error())
		return
	}
	h.buildAndRun(w, name, targetDir, stack, port, "upload", header.Filename)
}

func (h *DeployHandlers) buildAndRun(w http.ResponseWriter, name, targetDir, stack, port, sourceType, sourceRef string) {
	if stack == "" || stack == "auto" {
		stack = detectStack(targetDir)
	}
	containerPort := containerPortForStack(stack)
	if err := writeDockerfileIfMissing(targetDir, stack, containerPort); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	imageTag := "vpscontrol-" + name
	containerName := "vpscontrol-" + name

	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, targetDir)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker build failed:\n"+out)
		return
	}

	envFile := filepath.Join(targetDir, ".env")
	runArgs := []string{"run", "-d", "--name", containerName, "--restart", "unless-stopped",
		"-p", port + ":" + containerPort}
	if _, err := os.Stat(envFile); err == nil {
		runArgs = append(runArgs, "--env-file", envFile)
	}
	runArgs = append(runArgs, imageTag)

	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "failed to start the container:\n"+out)
		return
	}

	dep := store.Deployment{
		ID:         randomID(),
		Name:       name,
		Stack:      stack,
		SourceType: sourceType,
		SourceRef:  sourceRef,
		Path:       targetDir,
		Port:       port,
		Container:  containerName,
		CreatedAt:  time.Now(),
	}
	if err := h.Store.AddDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, dep)
}

func (h *DeployHandlers) List(w http.ResponseWriter, r *http.Request) {
	middleware.JSON(w, http.StatusOK, h.Store.ListDeployments())
}

func (h *DeployHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromPath("/api/deployments/", r.URL.Path)
	deployments := h.Store.ListDeployments()
	var target *store.Deployment
	for i := range deployments {
		if deployments[i].Name == name {
			target = &deployments[i]
			break
		}
	}
	if target == nil {
		middleware.JSONError(w, http.StatusNotFound, "deployment not found")
		return
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", target.Container)
	removeFiles := r.URL.Query().Get("removeFiles") == "true"
	if removeFiles && target.Path != "" {
		_ = os.RemoveAll(target.Path)
	}
	if err := h.Store.DeleteDeployment(name); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *DeployHandlers) Redeploy(w http.ResponseWriter, r *http.Request) {
	name := nameFromPath("/api/deployments/", strings.TrimSuffix(r.URL.Path, "/redeploy"))
	deployments := h.Store.ListDeployments()
	var target *store.Deployment
	for i := range deployments {
		if deployments[i].Name == name {
			target = &deployments[i]
			break
		}
	}
	if target == nil {
		middleware.JSONError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if target.SourceType == "git" {
		if out, err := runCommand(2*time.Minute, "git", "-C", target.Path, "pull"); err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, "git pull failed:\n"+out)
			return
		}
	}
	imageTag := "vpscontrol-" + target.Name
	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, target.Path)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "build failed:\n"+out)
		return
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", target.Container)
	containerPort := containerPortForStack(target.Stack)
	runArgs := []string{"run", "-d", "--name", target.Container, "--restart", "unless-stopped",
		"-p", target.Port + ":" + containerPort, imageTag}
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "restart failed:\n"+out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		fpathAbs, err := filepath.Abs(fpath)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(fpathAbs, destAbs+string(os.PathSeparator)) && fpathAbs != destAbs {
			return errors.New("invalid zip archive (path outside of the target directory)")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpathAbs, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpathAbs), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(fpathAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
