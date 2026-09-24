package handlers

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type DeployHandlers struct {
	Store      *store.Store
	DeployRoot string
}

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

var allowedNodeVersions = map[string]bool{
	"20": true,
	"21": true,
	"22": true,
	"23": true,
	"24": true,
}
var allowedPythonVersions = map[string]bool{"3.10": true, "3.11": true, "3.12": true, "3.13": true}
var allowedPHPVersions = map[string]bool{"8.1": true, "8.2": true, "8.3": true, "8.4": true}

const maxUploadSize = 30 * 1024 * 1024

func normalizeNodeVersion(v string) string {
	if allowedNodeVersions[v] {
		return v
	}
	return "22"
}
func normalizePythonVersion(v string) string {
	if allowedPythonVersions[v] {
		return v
	}
	return "3.12"
}
func normalizePHPVersion(v string) string {
	if allowedPHPVersions[v] {
		return v
	}
	return "8.3"
}

// =====================================================================
// Détection de la racine de l'app
// =====================================================================

var excludedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	"__pycache__":  true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
	"venv":         true,
	".venv":        true,
	".cache":       true,
	".idea":        true,
	".vscode":      true,
}

type markerFile struct {
	name     string
	priority int
}

var knownMarkers = []markerFile{
	{"Dockerfile", 1},
	{"package.json", 2},
	{"composer.json", 3},
	{"requirements.txt", 4},
	{"pyproject.toml", 4},
	{"manage.py", 5},
	{"app.py", 5},
	{"go.mod", 6},
	{"index.html", 7},
	{"index.htm", 7},
}

type candidate struct {
	subdir   string
	depth    int
	priority int
	liftable bool
}

func hasPackageStartScript(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts["start"]
	return ok
}

func hasLaravelArtisan(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "artisan"))
	return err == nil
}

func isLiftable(dir, marker string) bool {
	switch marker {
	case "package.json":
		return hasPackageStartScript(dir)
	case "composer.json":
		return hasLaravelArtisan(dir)
	case "requirements.txt", "pyproject.toml", "manage.py", "app.py":
		return true
	case "Dockerfile", "go.mod":
		return true
	case "index.html", "index.htm":
		return true
	}
	return false
}

func findAppRoot(root string) string {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}

	type queueItem struct {
		abs   string
		rel   string
		depth int
	}

	queue := []queueItem{{abs: rootAbs, rel: "", depth: 0}}
	var best *candidate

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if best != nil && cur.depth > best.depth {
			break
		}

		entries, err := os.ReadDir(cur.abs)
		if err != nil {
			continue
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})

		for _, m := range knownMarkers {
			for _, e := range entries {
				if e.IsDir() || e.Name() != m.name {
					continue
				}
				c := candidate{
					subdir:   cur.rel,
					depth:    cur.depth,
					priority: m.priority,
					liftable: isLiftable(cur.abs, m.name),
				}
				if best == nil || betterCandidate(c, *best) {
					best = &c
				}
				break
			}
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if excludedDirs[name] {
				continue
			}
			if strings.HasPrefix(name, ".") && name != ".env" {
				continue
			}
			childAbs := filepath.Join(cur.abs, name)
			childRel := name
			if cur.rel != "" {
				childRel = filepath.Join(cur.rel, name)
			}
			queue = append(queue, queueItem{
				abs:   childAbs,
				rel:   childRel,
				depth: cur.depth + 1,
			})
		}
	}

	if best == nil {
		return ""
	}
	return filepath.ToSlash(best.subdir)
}

func betterCandidate(a, b candidate) bool {
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	if a.liftable != b.liftable {
		return a.liftable
	}
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	return a.subdir < b.subdir
}

func appRoot(dep store.Deployment) string {
	if dep.AppSubdir == "" {
		return dep.Path
	}
	clean := filepath.Clean("/" + dep.AppSubdir)
	return filepath.Join(dep.Path, clean)
}

func resolveSubdirInput(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" || input == "." || input == "/" {
		return "", nil
	}
	input = strings.ReplaceAll(input, "\\", "/")
	clean := filepath.Clean("/" + input)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", nil
	}
	if strings.HasPrefix(clean, "..") {
		return "", errors.New("subdirectory cannot escape the app root")
	}
	return filepath.ToSlash(clean), nil
}

// =====================================================================
// Dockerfiles — modèle Wings
//
// Aucun COPY. Le code est monté en volume depuis l'hôte.
// Le CMD installe les dépendances au premier démarrage si nécessaire,
// puis lance l'app. Le résultat de l'install (node_modules, etc.) est
// écrit dans le volume → visible dans le panel.
// =====================================================================

func dockerfileNode(nodeVersion, port string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%s
EXPOSE %s
# Install au premier démarrage si node_modules n'existe pas encore,
# puis lance l'app. Le code vient du volume monté à /app.
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install --omit=dev || npm install; fi && npm start"]
`, nodeVersion, port, port)
}

func dockerfileNext(nodeVersion, port string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%s
EXPOSE %s
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install; fi && if [ ! -d .next ]; then npm run build; fi && npm start"]
`, nodeVersion, port, port)
}

func dockerfileNuxt(nodeVersion, port string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%s
EXPOSE %s
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install; fi && if [ ! -d .output ]; then npm run build; fi && node .output/server/index.mjs"]
`, nodeVersion, port, port)
}

func dockerfileReact(nodeVersion, buildDir string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
RUN apk add --no-cache nginx
# Build le SPA au premier démarrage si nécessaire, puis sert via Nginx.
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install; fi && if [ ! -d %s ]; then npm run build; fi && cp -r %s/* /usr/share/nginx/html/ && nginx -g 'daemon off;'"]
`, nodeVersion, buildDir, buildDir)
}

func dockerfileAstro(nodeVersion string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
RUN apk add --no-cache nginx
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install; fi && if [ ! -d dist ]; then npm run build; fi && cp -r dist/* /usr/share/nginx/html/ && nginx -g 'daemon off;'"]
`, nodeVersion)
}

func dockerfileSvelteKit(nodeVersion string) string {
	return fmt.Sprintf(`FROM node:%s-alpine
WORKDIR /app
ENV NODE_ENV=production
RUN apk add --no-cache nginx
CMD ["sh", "-c", "if [ ! -d node_modules ]; then npm install; fi && if [ ! -d build ]; then npm run build; fi && cp -r build/* /usr/share/nginx/html/ && nginx -g 'daemon off;'"]
`, nodeVersion)
}

func dockerfilePython(pythonVersion, port string) string {
	return fmt.Sprintf(`FROM python:%s-slim
WORKDIR /app
ENV PYTHONUNBUFFERED=1
ENV PORT=%s
EXPOSE %s
CMD ["sh", "-c", "if [ -f requirements.txt ] && [ ! -d .venv-installed ]; then pip install --no-cache-dir -r requirements.txt && touch .venv-installed; fi && python app.py"]
`, pythonVersion, port, port)
}

func dockerfileLaravelPHP(phpVersion string) string {
	return fmt.Sprintf(`FROM php:%s-apache
RUN apt-get update && apt-get install -y \
    git unzip libzip-dev libpng-dev libonig-dev libxml2-dev \
 && docker-php-ext-install pdo pdo_mysql mbstring zip bcmath gd \
 && a2enmod rewrite \
 && rm -rf /var/lib/apt/lists/*
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
WORKDIR /var/www/html
ENV APACHE_DOCUMENT_ROOT=/var/www/html/public
RUN sed -ri 's!/var/www/html!${APACHE_DOCUMENT_ROOT}!g' /etc/apache2/sites-available/*.conf \
 && sed -ri 's!/var/www/!${APACHE_DOCUMENT_ROOT}!g' /etc/apache2/apache2.conf /etc/apache2/conf-available/*.conf
EXPOSE 80
CMD ["sh", "-c", "if [ -f composer.json ] && [ ! -d vendor ]; then composer install --no-dev --optimize-autoloader || true; fi && apache2-foreground"]
`, phpVersion)
}

func dockerfileGo(port string) string {
	return fmt.Sprintf(`FROM golang:1.22-alpine
WORKDIR /app
ENV PORT=%s
EXPOSE %s
CMD ["sh", "-c", "if [ ! -f server ]; then go build -o server . ; fi && ./server"]
`, port, port)
}

func dockerfileStatic() string {
	return `FROM nginx:alpine
WORKDIR /usr/share/nginx/html
EXPOSE 80
# Nginx lit directement les fichiers du volume monté.
# Pas de COPY, pas de build.
`
}

// =====================================================================
// Détection de stack
// =====================================================================

func detectStack(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if has("package.json") {
		for _, cfg := range []string{"next.config.js", "next.config.mjs", "next.config.ts"} {
			if has(cfg) {
				if nextIsStaticExport(dir) {
					return "static"
				}
				return "next"
			}
		}
		if has("nuxt.config.js") || has("nuxt.config.ts") || has("nuxt.config.mjs") {
			return "nuxt"
		}
		if has("svelte.config.js") {
			if svelteKitIsStatic(dir) {
				return "sveltekit"
			}
			return "node"
		}
		if has("astro.config.mjs") || has("astro.config.js") || has("astro.config.ts") {
			return "astro"
		}
		for _, cfg := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs"} {
			if has(cfg) {
				return "react"
			}
		}
		if isReactProject(dir) {
			return "react"
		}
		return "node"
	}
	switch {
	case has("composer.json"):
		return "laravel"
	case has("requirements.txt") || has("app.py") || has("manage.py"):
		return "python"
	case has("go.mod"):
		return "go"
	case has("index.html") || has("index.htm"):
		return "static"
	default:
		return "static"
	}
}

func isReactProject(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	hasDep := func(name string) bool {
		_, ok1 := pkg.Dependencies[name]
		_, ok2 := pkg.DevDependencies[name]
		return ok1 || ok2
	}
	if hasDep("express") || hasDep("fastify") || hasDep("koa") || hasDep("hapi") {
		return false
	}
	return hasDep("react") || hasDep("react-dom")
}

func nextIsStaticExport(dir string) bool {
	for _, name := range []string{"next.config.js", "next.config.mjs", "next.config.ts"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, "output: 'export'") || strings.Contains(s, `output: "export"`) {
			return true
		}
	}
	return false
}

func svelteKitIsStatic(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "svelte.config.js"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "@sveltejs/adapter-static")
}

func reactBuildDir(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if has("vite.config.js") || has("vite.config.ts") || has("vite.config.mjs") {
		return "dist"
	}
	if has("src/index.js") || has("src/index.tsx") {
		return "build"
	}
	return "dist"
}

// =====================================================================
// Build options
// =====================================================================

type buildOptions struct {
	Stack         string
	NodeVersion   string
	PythonVersion string
	PHPVersion    string
	HostPort      string
	InternalPort  string
}

func writeDockerfile(dir string, opts buildOptions) error {
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	internal := opts.InternalPort
	if internal == "" {
		internal = opts.HostPort
	}
	var content string
	switch opts.Stack {
	case "laravel":
		content = dockerfileLaravelPHP(opts.PHPVersion)
	case "node":
		content = dockerfileNode(opts.NodeVersion, internal)
	case "react":
		content = dockerfileReact(opts.NodeVersion, reactBuildDir(dir))
	case "next":
		content = dockerfileNext(opts.NodeVersion, internal)
	case "astro":
		content = dockerfileAstro(opts.NodeVersion)
	case "sveltekit":
		content = dockerfileSvelteKit(opts.NodeVersion)
	case "nuxt":
		content = dockerfileNuxt(opts.NodeVersion, internal)
	case "python":
		content = dockerfilePython(opts.PythonVersion, internal)
	case "go":
		content = dockerfileGo(internal)
	default:
		content = dockerfileStatic()
	}
	return os.WriteFile(dockerfilePath, []byte(content), 0o644)
}

func writeDockerfileIfMissing(dir string, opts buildOptions) error {
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err == nil {
		return nil
	}
	return writeDockerfile(dir, opts)
}

func containerPortForStack(stack string) string {
	switch stack {
	case "laravel":
		return "80"
	case "node", "next", "nuxt":
		return "3000"
	case "react", "astro", "sveltekit":
		return "80"
	case "python":
		return "5000"
	case "go":
		return "8080"
	default:
		return "80"
	}
}

// =====================================================================
// SuggestPort
// =====================================================================

func (h *DeployHandlers) SuggestPort(w http.ResponseWriter, r *http.Request) {
	stack := r.URL.Query().Get("stack")
	repoURL := r.URL.Query().Get("repoUrl")

	internal := ""
	source := ""
	if repoURL != "" {
		tmpDir, err := os.MkdirTemp("", "vpscontrol-portdetect-*")
		if err == nil {
			defer os.RemoveAll(tmpDir)
			if _, err := runCommand(30*time.Second, "git", "clone", "--depth", "1", repoURL, tmpDir); err == nil {
				sub := findAppRoot(tmpDir)
				appDir := tmpDir
				if sub != "" {
					appDir = filepath.Join(tmpDir, sub)
				}
				if stack == "" || stack == "auto" {
					stack = detectStack(appDir)
				}
				det := detectPort(appDir, stack)
				internal = det.Port
				source = det.Source
			}
		}
	}
	if internal == "" {
		if stack == "" || stack == "auto" {
			stack = "static"
		}
		det := detectPort("", stack)
		internal = det.Port
		source = det.Source
	}
	hostPort, auto, err := suggestHostPort("", 8080)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"stack":        stack,
		"internalPort": internal,
		"hostPort":     hostPort,
		"autoAssigned": auto,
		"source":       source,
	})
}

// =====================================================================
// DeployGit
// =====================================================================

type deployGitRequest struct {
	Name           string `json:"name"`
	ServerID       string `json:"serverId"`
	RepoURL        string `json:"repoUrl"`
	Stack          string `json:"stack"`
	Port           string `json:"port"`
	NodeVersion    string `json:"nodeVersion"`
	PythonVersion  string `json:"pythonVersion"`
	PHPVersion     string `json:"phpVersion"`
	AppSubdir      string `json:"appSubdir"`
	UseGithubToken bool   `json:"useGithubToken"`
}

func (h *DeployHandlers) DeployGit(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())

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

	sh := &ServerHandlers{Store: h.Store}
	serverID, err := sh.resolveServerID(user, req.ServerID)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ok, msg := sh.checkServerQuota(serverID); !ok {
		middleware.JSONError(w, http.StatusConflict, msg)
		return
	}

	autoPort := req.Port == "" || req.Port == "auto"
	if !autoPort {
		if _, err := strconv.Atoi(req.Port); err != nil {
			middleware.JSONError(w, http.StatusBadRequest, "invalid port")
			return
		}
		if !portIsFree(req.Port) {
			middleware.JSONError(w, http.StatusConflict,
				fmt.Sprintf("port %s is already in use on this VPS, pick another one", req.Port))
			return
		}
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

	cloneURL := req.RepoURL
	cleanURL := req.RepoURL
	var ghToken string
	if req.UseGithubToken {
		g, ok := h.Store.GetGithubToken(user.ID)
		if !ok {
			middleware.JSONError(w, http.StatusForbidden, "GitHub account not connected")
			return
		}
		ghToken = g.Token
		cleanURL = strings.TrimPrefix(cleanURL, "https://")
		cleanURL = strings.TrimPrefix(cleanURL, "http://")
		if !strings.HasPrefix(cleanURL, "github.com/") {
			middleware.JSONError(w, http.StatusBadRequest, "useGithubToken requires a github.com URL")
			return
		}
		cloneURL = "https://x-access-token:" + ghToken + "@" + cleanURL
	}

	out, err := runCommand(2*time.Minute, "git", "-c", "credential.helper=", "clone", "--depth", "1", cloneURL, targetDir)
	if err != nil {
		_ = os.RemoveAll(targetDir)
		safeOut := out
		if ghToken != "" {
			safeOut = strings.ReplaceAll(safeOut, ghToken, "***")
			safeOut = strings.ReplaceAll(safeOut, "x-access-token", "***")
		}
		middleware.JSONError(w, http.StatusBadRequest, "git clone failed: "+truncateError(safeOut))
		return
	}
	if req.UseGithubToken {
		_, _ = runCommand(10*time.Second, "git", "-C", targetDir, "remote", "set-url", "origin",
			"https://"+cleanURL)
	}

	if autoPort {
		p, err := findFreePort(8080, 8200)
		if err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		req.Port = p
	}

	subdir, err := resolveSubdirInput(req.AppSubdir)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if subdir == "" {
		subdir = findAppRoot(targetDir)
	}
	effectiveDir := targetDir
	if subdir != "" {
		effectiveDir = filepath.Join(targetDir, subdir)
	}
	if _, err := os.Stat(effectiveDir); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "app subdirectory not found: "+subdir)
		return
	}

	opts := buildOptions{
		Stack:         req.Stack,
		NodeVersion:   normalizeNodeVersion(req.NodeVersion),
		PythonVersion: normalizePythonVersion(req.PythonVersion),
		PHPVersion:    normalizePHPVersion(req.PHPVersion),
		HostPort:      req.Port,
	}
	if req.UseGithubToken {
		_ = h.Store.TouchGithubToken(user.ID)
	}
	h.buildAndRun(w, req.Name, serverID, targetDir, effectiveDir, subdir, opts, "git", cleanURL, user.ID)
}

// =====================================================================
// DeployUpload → DRAFT
// =====================================================================

func (h *DeployHandlers) DeployUpload(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "upload too large or invalid")
		return
	}
	name := r.FormValue("name")
	serverID := r.FormValue("serverId")
	port := r.FormValue("port")

	if !appNameRe.MatchString(name) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid name (lowercase letters, digits, dashes, 2-40 characters)")
		return
	}

	sh := &ServerHandlers{Store: h.Store}
	validServerID, err := sh.resolveServerID(user, serverID)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ok, msg := sh.checkServerQuota(validServerID); !ok {
		middleware.JSONError(w, http.StatusConflict, msg)
		return
	}

	autoPort := port == "" || port == "auto"
	if !autoPort {
		if _, err := strconv.Atoi(port); err != nil {
			middleware.JSONError(w, http.StatusBadRequest, "invalid port")
			return
		}
		if !portIsFree(port) {
			middleware.JSONError(w, http.StatusConflict,
				fmt.Sprintf("port %s is already in use on this VPS, pick another one", port))
			return
		}
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
	if autoPort {
		p, err := findFreePort(8080, 8200)
		if err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		port = p
	}

	subdir := findAppRoot(targetDir)

	dep := store.Deployment{
		ID:          randomID(),
		Name:        name,
		Stack:       "auto",
		SourceType:  "upload",
		SourceRef:   header.Filename,
		Path:        targetDir,
		AppSubdir:   subdir,
		ServerID:    validServerID,
		Port:        port,
		Container:   "vpscontrol-" + name,
		OwnerID:     user.ID,
		Status:      "draft",
		Allocations: []store.Allocation{
			{ID: randomID(), Port: port, Primary: true, CreatedAt: time.Now()},
		},
		CreatedAt: time.Now(),
	}
	if err := h.Store.AddDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "deploy.draft",
		Target:    name,
		Details:   map[string]interface{}{"source": "zip", "file": header.Filename, "appSubdir": subdir, "serverId": validServerID},
	})

	middleware.JSON(w, http.StatusCreated, dep)
}

// =====================================================================
// DeployDraft
// =====================================================================

func (h *DeployHandlers) DeployDraft(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	sh := &ServerHandlers{Store: h.Store}
	if dep.ServerID != "" {
		if ok, msg := sh.checkServerQuota(dep.ServerID); !ok {
			middleware.JSONError(w, http.StatusConflict, msg)
			return
		}
	}

	if dep.AppSubdir == "" {
		dep.AppSubdir = findAppRoot(dep.Path)
	}
	effDir := appRoot(dep)
	if _, err := os.Stat(effDir); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "app root not found: "+effDir)
		return
	}

	stack := detectStack(effDir)
	dep.Stack = stack

	internalPort := detectPort(effDir, stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(stack)
	}

	opts := buildOptions{
		Stack:         stack,
		NodeVersion:   normalizeNodeVersion(dep.NodeVersion),
		PythonVersion: normalizePythonVersion(dep.PythonVersion),
		PHPVersion:    normalizePHPVersion(dep.PHPVersion),
		HostPort:      dep.Port,
		InternalPort:  internalPort,
	}
	if err := writeDockerfileIfMissing(effDir, opts); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "failed to write Dockerfile: "+err.Error())
		return
	}

	dep.Status = "deploying"
	dep.LastError = ""
	_ = h.Store.UpdateDeployment(dep)

	imageTag := "vpscontrol-" + dep.Name
	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, effDir)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("docker build failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)

	runArgs := buildRunArgs(dep, internalPort)
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("failed to start container:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	dep.Status = "running"
	_ = h.Store.UpdateDeployment(dep)

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "deploy.start",
		Target:    dep.Name,
		Details:   map[string]interface{}{"stack": stack, "port": dep.Port, "appSubdir": dep.AppSubdir, "serverId": dep.ServerID},
	})

	middleware.JSON(w, http.StatusOK, dep)
}

// =====================================================================
// buildAndRun
// =====================================================================

func (h *DeployHandlers) buildAndRun(w http.ResponseWriter, name, serverID, targetDir, effectiveDir, appSubdir string, opts buildOptions, sourceType, sourceRef, ownerID string) {
	stack := opts.Stack
	if stack == "" || stack == "auto" {
		stack = detectStack(effectiveDir)
	}
	opts.Stack = stack

	det := detectPort(effectiveDir, stack)
	if opts.InternalPort == "" {
		opts.InternalPort = det.Port
	}
	if opts.InternalPort == "" {
		opts.InternalPort = containerPortForStack(stack)
	}

	if err := writeDockerfileIfMissing(effectiveDir, opts); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	imageTag := "vpscontrol-" + name
	containerName := "vpscontrol-" + name

	dep := store.Deployment{
		ID:            randomID(),
		Name:          name,
		Stack:         stack,
		SourceType:    sourceType,
		SourceRef:     sourceRef,
		Path:          targetDir,
		AppSubdir:     appSubdir,
		ServerID:      serverID,
		Port:          opts.HostPort,
		Container:     containerName,
		NodeVersion:   opts.NodeVersion,
		PythonVersion: opts.PythonVersion,
		PHPVersion:    opts.PHPVersion,
		OwnerID:       ownerID,
		Status:        "deploying",
		Allocations: []store.Allocation{
			{ID: randomID(), Port: opts.HostPort, Primary: true, CreatedAt: time.Now()},
		},
		CreatedAt: time.Now(),
	}

	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, effectiveDir)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("docker build failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.AddDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	runArgs := buildRunArgs(dep, opts.InternalPort)
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("failed to start container:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.AddDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	dep.Status = "running"
	if err := h.Store.AddDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, dep)
}

// =====================================================================
// List / Get / Delete
// =====================================================================

func (h *DeployHandlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	deps := h.Store.ListDeploymentsForUser(user.ID, user.Role)

	for i := range deps {
		h.enrichStatus(&deps[i])
	}
	middleware.JSON(w, http.StatusOK, deps)
}

func (h *DeployHandlers) Get(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	perms, _ := middleware.PermissionsFromContext(r.Context())
	h.enrichStatus(&dep)

	serverName := ""
	if dep.ServerID != "" {
		if srv, ok := h.Store.FindServerByID(dep.ServerID); ok {
			serverName = srv.Name
		}
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"deployment":  dep,
		"permissions": perms,
		"serverName":  serverName,
	})
}

func (h *DeployHandlers) enrichStatus(dep *store.Deployment) {
	if dep.Status == "draft" {
		return
	}
	statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
		"--format", "{{.State.Status}}|{{.State.ExitCode}}", dep.Container)
	parts := strings.Split(strings.TrimSpace(statusOut), "|")
	if len(parts) < 2 {
		return
	}
	dockerStatus := parts[0]
	exitCode, _ := strconv.Atoi(parts[1])

	switch dockerStatus {
	case "running":
		if dep.Status != "running" {
			dep.Status = "running"
			dep.LastError = ""
			_ = h.Store.UpdateDeployment(*dep)
		}
	case "exited":
		if exitCode != 0 && dep.Status != "error" {
			logs, _ := runCommand(5*time.Second, "docker", "logs", "--tail", "50", dep.Container)
			dep.Status = "error"
			dep.LastError = truncateError(logs)
			dep.LastErrorAt = time.Now()
			_ = h.Store.UpdateDeployment(*dep)
		} else if exitCode == 0 && dep.Status != "stopped" {
			dep.Status = "stopped"
			_ = h.Store.UpdateDeployment(*dep)
		}
	}
}

func (h *DeployHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)
	removeFiles := r.URL.Query().Get("removeFiles") == "true"
	if removeFiles && dep.Path != "" {
		_ = removeAllSafe(dep.Path)
	}
	if err := h.Store.DeleteDeployment(dep.ID); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// =====================================================================
// Redeploy
// =====================================================================

func (h *DeployHandlers) Redeploy(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	if dep.SourceType == "git" {
		if out, err := runCommand(2*time.Minute, "git", "-C", dep.Path, "pull"); err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, "git pull failed:\n"+out)
			return
		}
	}

	if dep.AppSubdir == "" {
		dep.AppSubdir = findAppRoot(dep.Path)
	}
	effDir := appRoot(dep)
	if _, err := os.Stat(effDir); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "app root not found: "+effDir)
		return
	}

	internalPort := detectPort(effDir, dep.Stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(dep.Stack)
	}

	opts := buildOptions{
		Stack:         dep.Stack,
		NodeVersion:   normalizeNodeVersion(dep.NodeVersion),
		PythonVersion: normalizePythonVersion(dep.PythonVersion),
		PHPVersion:    normalizePHPVersion(dep.PHPVersion),
		HostPort:      dep.Port,
		InternalPort:  internalPort,
	}
	if err := writeDockerfile(effDir, opts); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	imageTag := "vpscontrol-" + dep.Name
	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, effDir)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("build failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)

	runArgs := buildRunArgs(dep, internalPort)
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("restart failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	dep.Status = "running"
	dep.LastError = ""
	_ = h.Store.UpdateDeployment(dep)
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// =====================================================================
// UpdateSettings
// =====================================================================

type updateSettingsRequest struct {
	Stack         string `json:"stack"`
	NodeVersion   string `json:"nodeVersion"`
	PythonVersion string `json:"pythonVersion"`
	PHPVersion    string `json:"phpVersion"`
	HostPort      string `json:"hostPort"`
	InternalPort  string `json:"internalPort"`
	AppSubdir     string `json:"appSubdir"`
}

func (h *DeployHandlers) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	validStacks := map[string]bool{
		"laravel": true, "node": true, "react": true, "next": true,
		"astro": true, "sveltekit": true, "nuxt": true, "python": true,
		"go": true, "static": true,
	}
	if req.Stack != "" && !validStacks[req.Stack] {
		middleware.JSONError(w, http.StatusBadRequest, "invalid stack")
		return
	}

	changed := false
	newStack := dep.Stack
	if req.Stack != "" && req.Stack != dep.Stack {
		newStack = req.Stack
		changed = true
	}
	newNodeVersion := dep.NodeVersion
	if req.NodeVersion != "" && req.NodeVersion != dep.NodeVersion {
		if !allowedNodeVersions[req.NodeVersion] {
			middleware.JSONError(w, http.StatusBadRequest, "invalid node version (allowed: 20, 21, 22, 23, 24)")
			return
		}
		newNodeVersion = req.NodeVersion
		changed = true
	}
	newPythonVersion := dep.PythonVersion
	if req.PythonVersion != "" && req.PythonVersion != dep.PythonVersion {
		if !allowedPythonVersions[req.PythonVersion] {
			middleware.JSONError(w, http.StatusBadRequest, "invalid python version")
			return
		}
		newPythonVersion = req.PythonVersion
		changed = true
	}
	newPHPVersion := dep.PHPVersion
	if req.PHPVersion != "" && req.PHPVersion != dep.PHPVersion {
		if !allowedPHPVersions[req.PHPVersion] {
			middleware.JSONError(w, http.StatusBadRequest, "invalid php version")
			return
		}
		newPHPVersion = req.PHPVersion
		changed = true
	}

	newHostPort := dep.Port
	if req.HostPort != "" && req.HostPort != dep.Port {
		if _, err := strconv.Atoi(req.HostPort); err != nil {
			middleware.JSONError(w, http.StatusBadRequest, "invalid host port")
			return
		}
		if !portIsFree(req.HostPort) {
			middleware.JSONError(w, http.StatusConflict,
				fmt.Sprintf("host port %s is already in use", req.HostPort))
			return
		}
		newHostPort = req.HostPort
		changed = true
	}

	newAppSubdir := dep.AppSubdir
	if req.AppSubdir == "auto" {
		newAppSubdir = findAppRoot(dep.Path)
		if newAppSubdir != dep.AppSubdir {
			changed = true
		}
	} else if req.AppSubdir != "" || dep.AppSubdir != "" {
		subdir, err := resolveSubdirInput(req.AppSubdir)
		if err != nil {
			middleware.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if subdir != dep.AppSubdir {
			newAppSubdir = subdir
			changed = true
		}
	}
	if newAppSubdir != "" {
		full := filepath.Join(dep.Path, newAppSubdir)
		if _, err := os.Stat(full); err != nil {
			middleware.JSONError(w, http.StatusBadRequest, "app subdirectory not found: "+newAppSubdir)
			return
		}
	}

	newInternalPort := req.InternalPort
	effDirForDetect := dep.Path
	if newAppSubdir != "" {
		effDirForDetect = filepath.Join(dep.Path, newAppSubdir)
	}
	if newInternalPort == "" {
		det := detectPort(effDirForDetect, newStack)
		newInternalPort = det.Port
		if newInternalPort == "" {
			newInternalPort = containerPortForStack(newStack)
		}
	}

	if !changed {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"message": "No changes detected.",
			"changed": false,
		})
		return
	}

	dep.Stack = newStack
	dep.NodeVersion = newNodeVersion
	dep.PythonVersion = newPythonVersion
	dep.PHPVersion = newPHPVersion
	dep.Port = newHostPort
	dep.AppSubdir = newAppSubdir

	if err := h.Store.UpdateDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	effDir := appRoot(dep)
	opts := buildOptions{
		Stack:         newStack,
		NodeVersion:   newNodeVersion,
		PythonVersion: newPythonVersion,
		PHPVersion:    newPHPVersion,
		HostPort:      newHostPort,
		InternalPort:  newInternalPort,
	}
	if err := writeDockerfile(effDir, opts); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "failed to regenerate Dockerfile: "+err.Error())
		return
	}

	imageTag := "vpscontrol-" + dep.Name
	out, err := runCommand(5*time.Minute, "docker", "build", "-t", imageTag, effDir)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("docker build failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)
	runArgs := buildRunArgs(dep, newInternalPort)
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("failed to restart the container:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	dep.Status = "running"
	_ = h.Store.UpdateDeployment(dep)
	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"message":      "Settings updated. Container rebuilt and restarted.",
		"changed":      true,
		"deployment":   dep,
		"internalPort": newInternalPort,
	})
}

// =====================================================================
// UpdateLimits
// =====================================================================

type updateLimitsRequest struct {
	CPUQuota  float64 `json:"cpuQuota"`
	MemoryMB  int     `json:"memoryMB"`
	DiskMB    int     `json:"diskMB"`
	PidsLimit int     `json:"pidsLimit"`
}

func (h *DeployHandlers) UpdateLimits(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req updateLimitsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.CPUQuota < 0 || req.CPUQuota > 10000 {
		middleware.JSONError(w, http.StatusBadRequest, "cpuQuota out of range")
		return
	}
	if req.MemoryMB < 0 || req.MemoryMB > 1024*1024 {
		middleware.JSONError(w, http.StatusBadRequest, "memoryMB out of range")
		return
	}
	if req.PidsLimit < 0 || req.PidsLimit > 100000 {
		middleware.JSONError(w, http.StatusBadRequest, "pidsLimit out of range")
		return
	}

	dep.Limits = store.Limits{
		CPUQuota:  req.CPUQuota,
		MemoryMB:  req.MemoryMB,
		DiskMB:    req.DiskMB,
		PidsLimit: req.PidsLimit,
	}
	if err := h.Store.UpdateDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	effDir := appRoot(dep)
	internalPort := detectPort(effDir, dep.Stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(dep.Stack)
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)
	runArgs := buildRunArgs(dep, internalPort)
	out, err := runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "limits saved but restart failed: "+out)
		return
	}
	middleware.JSON(w, http.StatusOK, dep.Limits)
}

// =====================================================================
// Console
// =====================================================================

func (h *DeployHandlers) ConsoleInfo(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
		"--format", "{{.State.Status}}|{{.State.ExitCode}}|{{.State.StartedAt}}|{{.State.FinishedAt}}",
		dep.Container)
	status := "unknown"
	startedAt := ""
	finishedAt := ""
	exitCode := 0
	parts := strings.Split(strings.TrimSpace(statusOut), "|")
	if len(parts) >= 1 && parts[0] != "" {
		status = parts[0]
	}
	if len(parts) >= 2 {
		exitCode, _ = strconv.Atoi(parts[1])
	}
	if len(parts) >= 3 {
		startedAt = parts[2]
	}
	if len(parts) >= 4 {
		finishedAt = parts[3]
	}

	logs := ""
	if status == "running" {
		logsOut, _ := runCommand(10*time.Second, "docker", "logs", "--tail", "100", dep.Container)
		logs = logsOut
	}

	if status == "exited" && exitCode != 0 {
		logsOut, _ := runCommand(10*time.Second, "docker", "logs", "--tail", "100", dep.Container)
		dep.Status = "error"
		dep.LastError = truncateError(logsOut)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"deploymentId": dep.ID,
		"container":    dep.Container,
		"status":       status,
		"exitCode":     exitCode,
		"startedAt":    startedAt,
		"finishedAt":   finishedAt,
		"logs":         logs,
		"lastError":    dep.LastError,
		"appStatus":    dep.Status,
	})
}

func (h *DeployHandlers) ConsoleStream(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		middleware.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
		"--format", "{{.State.Status}}", dep.Container)
	if strings.TrimSpace(statusOut) != "running" {
		flusher.Flush()
		return
	}

	ctx := r.Context()
	cmd, stdout, err := runStreamCommand(ctx, "docker", "logs", "-f", "--tail", "50", dep.Container)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

type consoleExecRequest struct {
	Command string `json:"command"`
}

func (h *DeployHandlers) ConsoleExec(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req consoleExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		middleware.JSONError(w, http.StatusBadRequest, "empty command")
		return
	}
	if len(cmd) > 2000 {
		middleware.JSONError(w, http.StatusBadRequest, "command too long (max 2000 chars)")
		return
	}

	statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
		"--format", "{{.State.Status}}", dep.Container)
	status := strings.TrimSpace(statusOut)
	if status != "running" {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"output":   "Container is not running (status: " + status + ").",
			"exitCode": -1,
		})
		return
	}

	out, err := runCommand(30*time.Second, "docker", "exec", dep.Container, "sh", "-c", cmd)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(interface{ ExitCode() int }); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"output":   out,
		"exitCode": exitCode,
	})
}

// =====================================================================
// Helpers
// =====================================================================

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

func truncateError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4000 {
		return s[:4000] + "\n... (truncated)"
	}
	return s
}