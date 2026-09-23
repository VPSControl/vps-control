// VPS Control — a lightweight web admin panel for VPS instances.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"vpscontrol/internal/auth"
	"vpscontrol/internal/handlers"
	"vpscontrol/internal/middleware"
	"vpscontrol/internal/scheduler"
	"vpscontrol/internal/store"
)

//go:embed web/*.html web/style.css web/app.js web/assets/*
var webFS embed.FS

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dataDir := env("VPSCONTROL_DATA_DIR", "/opt/vpscontrol/data")
	filesRoot := env("VPSCONTROL_FILES_ROOT", "/home")
	deployRoot := env("VPSCONTROL_DEPLOY_ROOT", "/opt/vpscontrol/apps")
	backupRoot := env("VPSCONTROL_BACKUP_ROOT", "/opt/vpscontrol/backups")
	listenAddr := env("VPSCONTROL_LISTEN", "127.0.0.1:8090")
	srcDir := env("VPSCONTROL_SRC_DIR", "/opt/vpscontrol-src")
	repoURL := env("VPSCONTROL_REPO_URL", "https://github.com/VPSControl/vps-control.git")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("could not create data directory %s: %v", dataDir, err)
	}
	if err := os.MkdirAll(deployRoot, 0o755); err != nil {
		log.Fatalf("could not create deploy directory %s: %v", deployRoot, err)
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		log.Fatalf("could not create backup directory %s: %v", backupRoot, err)
	}

	st, err := store.New(dataDir)
	if err != nil {
		log.Fatalf("storage error: %v", err)
	}

	secret, err := loadOrCreateSecret(dataDir)
	if err != nil {
		log.Fatalf("session secret error: %v", err)
	}

	// ---- Handlers ----
	authH := &handlers.AuthHandlers{Store: st, Secret: secret}
	userH := &handlers.UserHandlers{Store: st}
	fileH := &handlers.FileHandlers{Root: filesRoot}
	svcH := &handlers.ServiceHandlers{}
	deployH := &handlers.DeployHandlers{Store: st, DeployRoot: deployRoot}
	dbH := &handlers.DatabaseHandlers{Store: st}
	sysH := &handlers.SystemHandlers{Store: st, SrcDir: srcDir, RepoURL: repoURL}
	hookH := &handlers.WebhookHandlers{Store: st, SrcDir: srcDir}
	subH := &handlers.SubuserHandlers{Store: st}
	backupH := &handlers.BackupHandlers{Store: st, BackupRoot: backupRoot}
	schedH := &handlers.ScheduleHandlers{Store: st, BackupRoot: backupRoot}
	envH := &handlers.EnvHandlers{Store: st}
	allocH := &handlers.AllocationHandlers{Store: st}
	domH := &handlers.DomainHandlers{Store: st, SitesAvailable: "/etc/nginx/sites-available", SitesEnabled: "/etc/nginx/sites-enabled"}
	actH := &handlers.ActivityHandlers{Store: st}
	tokH := &handlers.APITokenHandlers{Store: st}
	statsH := &handlers.AppStatsHandlers{}
	ghH := &handlers.GithubHandlers{Store: st, DeployRoot: deployRoot}
	srvH := &handlers.ServerHandlers{Store: st}

	// ---- Scheduler ----
	runner := func(sc store.Schedule) (string, error) {
		return runSchedule(st, backupRoot, sc)
	}
	sched := scheduler.New(st, runner)
	schedH.TriggerFunc = func(action string) error {
		if action == "reload" {
			sched.Reload()
			return nil
		}
		return sched.RunNow(action)
	}
	sched.Start()
	defer sched.Stop()

	mux := http.NewServeMux()

	// =====================================================================
	// Public
	// =====================================================================
	mux.HandleFunc("/api/setup/status", methodGuard("GET", authH.SetupStatus))
	mux.HandleFunc("/api/setup", methodGuard("POST", authH.Setup))
	mux.HandleFunc("/api/login", methodGuard("POST", authH.Login))
	mux.HandleFunc("/api/webhook/github", hookH.GitHub)

	sessionAuth := middleware.Auth(secret, st)
	authMw := middleware.APITokenAuth(st, sessionAuth)
	adminMw := middleware.RequireAdmin

	depAccess := middleware.RequireDeploymentAccess(st)
	permConsole := middleware.RequirePermission("console")
	permSettings := middleware.RequirePermission("settings")
	permBackup := middleware.RequirePermission("backup")
	permFilesRead := middleware.RequirePermission("files.read")
	permFilesWrite := middleware.RequirePermission("files.write")

	// =====================================================================
	// Auth
	// =====================================================================
	mux.Handle("/api/logout", authMw(http.HandlerFunc(authH.Logout)))
	mux.Handle("/api/me", authMw(http.HandlerFunc(authH.Me)))
	mux.Handle("/api/invite/", authMw(http.HandlerFunc(subH.AcceptInvite)))

	// =====================================================================
	// API tokens
	// =====================================================================
	mux.Handle("/api/tokens", authMw(methodSplit(map[string]http.HandlerFunc{
		"GET": tokH.List, "POST": tokH.Create,
	})))
	mux.Handle("/api/tokens/", authMw(http.HandlerFunc(tokH.Revoke)))

	// =====================================================================
	// GitHub (par utilisateur)
	// =====================================================================
	mux.Handle("/api/github/status", authMw(http.HandlerFunc(ghH.Status)))
	mux.Handle("/api/github/token", authMw(methodSplit(map[string]http.HandlerFunc{
		"POST":   ghH.SetToken,
		"DELETE": ghH.DeleteToken,
	})))
	mux.Handle("/api/github/repos", authMw(http.HandlerFunc(ghH.ListRepos)))
	mux.Handle("/api/github/import", authMw(http.HandlerFunc(ghH.Import)))

	// =====================================================================
	// Servers — user (ses propres serveurs)
	// =====================================================================
	mux.Handle("/api/servers/mine", authMw(http.HandlerFunc(srvH.Mine)))

	// =====================================================================
	// Servers — admin (tous les serveurs)
	// =====================================================================
	mux.Handle("/api/admin/servers", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET":  srvH.ListAll,
		"POST": srvH.Create,
	}))))
	mux.Handle("/api/admin/servers/", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"PUT":    srvH.Update,
		"DELETE": srvH.Delete,
	}))))

	// =====================================================================
	// Activity
	// =====================================================================
	mux.Handle("/api/activity", authMw(http.HandlerFunc(actH.List)))

	// =====================================================================
	// Admin — users
	// =====================================================================
	mux.Handle("/api/users", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET": userH.List, "POST": userH.Create,
	}))))
	mux.Handle("/api/users/", authMw(adminMw(http.HandlerFunc(userH.Delete))))

	// L4 : endpoints admin avec preview + cascade propre.
	mux.Handle("/api/admin/users/", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET":    userH.Preview,
		"DELETE": userH.Delete,
	}))))

	// =====================================================================
	// Admin — global files
	// =====================================================================
	mux.Handle("/api/files", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET": fileH.List, "DELETE": fileH.Delete,
	}))))
	mux.Handle("/api/files/content", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET": fileH.ReadContent, "PUT": fileH.WriteContent,
	}))))
	mux.Handle("/api/files/create", authMw(adminMw(http.HandlerFunc(fileH.CreateFile))))
	mux.Handle("/api/files/mkdir", authMw(adminMw(http.HandlerFunc(fileH.Mkdir))))
	mux.Handle("/api/files/rename", authMw(adminMw(http.HandlerFunc(fileH.Rename))))
	mux.Handle("/api/files/move", authMw(adminMw(http.HandlerFunc(fileH.Move))))
	mux.Handle("/api/files/delete-many", authMw(adminMw(http.HandlerFunc(fileH.DeleteMany))))
	mux.Handle("/api/files/upload", authMw(adminMw(http.HandlerFunc(fileH.Upload))))
	mux.Handle("/api/files/download", authMw(adminMw(http.HandlerFunc(fileH.Download))))
	mux.Handle("/api/files/download-zip", authMw(adminMw(http.HandlerFunc(fileH.DownloadZip))))
	mux.Handle("/api/files/extract", authMw(adminMw(http.HandlerFunc(fileH.Extract))))
	mux.Handle("/api/files/compress", authMw(adminMw(http.HandlerFunc(fileH.Compress))))

	// =====================================================================
	// Admin — docker services
	// =====================================================================
	mux.Handle("/api/services", authMw(adminMw(http.HandlerFunc(svcH.List))))
	mux.Handle("/api/services/", authMw(adminMw(http.HandlerFunc(serviceDispatch(svcH)))))

	// =====================================================================
	// Admin — databases
	// =====================================================================
	mux.Handle("/api/db/connections", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET": dbH.ListConnections, "POST": dbH.AddConnection,
	}))))
	mux.Handle("/api/db/connections/", authMw(adminMw(http.HandlerFunc(dbDispatch(dbH)))))

	// =====================================================================
	// Admin — system
	// =====================================================================
	mux.Handle("/api/system/info", authMw(adminMw(http.HandlerFunc(sysH.Info))))
	mux.Handle("/api/system/stats", authMw(http.HandlerFunc(sysH.Stats)))
	mux.Handle("/api/system/check-update", authMw(adminMw(http.HandlerFunc(sysH.CheckUpdate))))
	mux.Handle("/api/system/update", authMw(adminMw(http.HandlerFunc(sysH.Update))))
	mux.Handle("/api/system/webhook/regenerate", authMw(adminMw(http.HandlerFunc(sysH.RegenerateWebhookSecret))))

	// =====================================================================
	// Deployments — création
	//
	// Accessible à TOUT utilisateur authentifié. L'isolation est garantie
	// en aval par resolveServerID() (refuse si le serveur n'appartient pas
	// à l'appelant) et checkServerQuota() (refuse si quota disque ou
	// maxApps dépassé).
	// =====================================================================
	mux.Handle("/api/deploy/git", authMw(http.HandlerFunc(deployH.DeployGit)))
	mux.Handle("/api/deploy/upload", authMw(http.HandlerFunc(deployH.DeployUpload)))
	mux.Handle("/api/deploy/suggest-port", authMw(http.HandlerFunc(deployH.SuggestPort)))
	mux.Handle("/api/deployments", authMw(http.HandlerFunc(deployH.List)))

	// =====================================================================
	// Deployments — routes scopées par app
	// =====================================================================
	mux.Handle("/api/deployments/", authMw(depAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		trimmed := strings.TrimPrefix(path, "/api/deployments/")

		switch {
		case r.Method == "GET" && !strings.Contains(trimmed, "/"):
			deployH.Get(w, r)
		case r.Method == "DELETE" && !strings.Contains(trimmed, "/"):
			permSettings(http.HandlerFunc(deployH.Delete)).ServeHTTP(w, r)

		case r.Method == "POST" && strings.HasSuffix(path, "/deploy"):
			permSettings(http.HandlerFunc(deployH.DeployDraft)).ServeHTTP(w, r)

		case r.Method == "POST" && strings.HasSuffix(path, "/redeploy"):
			permSettings(http.HandlerFunc(deployH.Redeploy)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.HasSuffix(path, "/settings"):
			permSettings(http.HandlerFunc(deployH.UpdateSettings)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.HasSuffix(path, "/limits"):
			permSettings(http.HandlerFunc(deployH.UpdateLimits)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/subusers"):
			permSettings(http.HandlerFunc(subH.List)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/invite"):
			permSettings(http.HandlerFunc(subH.Invite)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/invitations"):
			permSettings(http.HandlerFunc(subH.ListInvitations)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.Contains(path, "/subusers/"):
			permSettings(http.HandlerFunc(subH.UpdatePermissions)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.Contains(path, "/subusers/"):
			permSettings(http.HandlerFunc(subH.Remove)).ServeHTTP(w, r)

		case r.Method == "POST" && strings.HasSuffix(path, "/console/exec"):
			permConsole(http.HandlerFunc(deployH.ConsoleExec)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/console/stream"):
			permConsole(http.HandlerFunc(deployH.ConsoleStream)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/console"):
			permConsole(http.HandlerFunc(deployH.ConsoleInfo)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/stats/live"):
			permConsole(http.HandlerFunc(statsH.Live)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/env"):
			permSettings(http.HandlerFunc(envH.List)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.HasSuffix(path, "/env"):
			permSettings(http.HandlerFunc(envH.Set)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/allocations"):
			permSettings(http.HandlerFunc(allocH.List)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/allocations"):
			permSettings(http.HandlerFunc(allocH.Create)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.Contains(path, "/allocations/"):
			permSettings(http.HandlerFunc(allocH.Delete)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/domains"):
			permSettings(http.HandlerFunc(domH.List)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/domains"):
			permSettings(http.HandlerFunc(domH.Create)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/ssl") && strings.Contains(path, "/domains/"):
			permSettings(http.HandlerFunc(domH.IssueSSL)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.Contains(path, "/domains/"):
			permSettings(http.HandlerFunc(domH.Delete)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/backups"):
			permBackup(http.HandlerFunc(backupH.List)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/backups"):
			permBackup(http.HandlerFunc(backupH.Create)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/download") && strings.Contains(path, "/backups/"):
			permBackup(http.HandlerFunc(backupH.Download)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/restore") && strings.Contains(path, "/backups/"):
			permBackup(http.HandlerFunc(backupH.Restore)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.Contains(path, "/backups/"):
			permBackup(http.HandlerFunc(backupH.Delete)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/schedules"):
			permSettings(http.HandlerFunc(schedH.List)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/schedules"):
			permSettings(http.HandlerFunc(schedH.Create)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/run") && strings.Contains(path, "/schedules/"):
			permSettings(http.HandlerFunc(schedH.Run)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.Contains(path, "/schedules/"):
			permSettings(http.HandlerFunc(schedH.Update)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.Contains(path, "/schedules/"):
			permSettings(http.HandlerFunc(schedH.Delete)).ServeHTTP(w, r)

		case r.Method == "GET" && strings.HasSuffix(path, "/files/content"):
			permFilesRead(http.HandlerFunc(fileH.AppReadContent)).ServeHTTP(w, r)
		case r.Method == "PUT" && strings.HasSuffix(path, "/files/content"):
			permFilesWrite(http.HandlerFunc(fileH.AppWriteContent)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/files/download"):
			permFilesRead(http.HandlerFunc(fileH.AppDownload)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/files/find"):
			permFilesRead(http.HandlerFunc(fileH.AppFindFile)).ServeHTTP(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/files"):
			permFilesRead(http.HandlerFunc(fileH.AppList)).ServeHTTP(w, r)
		case r.Method == "DELETE" && strings.HasSuffix(path, "/files"):
			permFilesWrite(http.HandlerFunc(fileH.AppDelete)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/upload"):
			permFilesWrite(http.HandlerFunc(fileH.AppUpload)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/create"):
			permFilesWrite(http.HandlerFunc(fileH.AppCreateFile)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/mkdir"):
			permFilesWrite(http.HandlerFunc(fileH.AppMkdir)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/rename"):
			permFilesWrite(http.HandlerFunc(fileH.AppRename)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/move"):
			permFilesWrite(http.HandlerFunc(fileH.AppMove)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/delete-many"):
			permFilesWrite(http.HandlerFunc(fileH.AppDeleteMany)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/download-zip"):
			permFilesRead(http.HandlerFunc(fileH.AppDownloadZip)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/extract"):
			permFilesWrite(http.HandlerFunc(fileH.AppExtract)).ServeHTTP(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/files/compress"):
			permFilesWrite(http.HandlerFunc(fileH.AppCompress)).ServeHTTP(w, r)

		default:
			middleware.JSONError(w, http.StatusNotFound, "unknown route")
		}
	}))))

	// =====================================================================
	// Invitation
	// =====================================================================
	mux.HandleFunc("/invite/", func(w http.ResponseWriter, r *http.Request) {
		b, err := webFS.ReadFile("web/invite.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})

	// =====================================================================
	// Static
	// =====================================================================
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			b, err := webFS.ReadFile("web/index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
			w.Write(b)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      logRequests(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("VPS Control listening on %s (data: %s, apps: %s, backups: %s)",
		listenAddr, dataDir, deployRoot, backupRoot)
	log.Fatal(srv.ListenAndServe())
}

// =====================================================================
// runCommand
// =====================================================================
func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// =====================================================================
// Scheduler runner
// =====================================================================
func runSchedule(st *store.Store, backupRoot string, sc store.Schedule) (string, error) {
	dep, ok := st.FindDeploymentByID(sc.DeploymentID)
	if !ok {
		return "", fmt.Errorf("deployment not found")
	}
	switch sc.Action {
	case "backup":
		return runBackup(st, backupRoot, dep, "scheduled")
	case "restart":
		out, err := runCommand(60*time.Second, "docker", "restart", dep.Container)
		return out, err
	case "command":
		if strings.TrimSpace(sc.Command) == "" {
			return "", fmt.Errorf("schedule has no command")
		}
		out, err := runCommand(60*time.Second, "docker", "exec", dep.Container, "sh", "-c", sc.Command)
		return out, err
	}
	return "", fmt.Errorf("unknown schedule action: %s", sc.Action)
}

func runBackup(st *store.Store, backupRoot string, dep store.Deployment, trigger string) (string, error) {
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return "", err
	}
	timestamp := time.Now().Format("20060102-150405")
	archiveName := dep.Name + "-" + timestamp + ".tar.gz"
	archivePath := filepath.Join(backupRoot, archiveName)

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
		return out, err
	}

	imageTag := "vpscontrol-" + dep.Name
	backupImageTag := "vpscontrol-backup-" + dep.Name + ":" + timestamp
	if _, err := runCommand(2*time.Minute, "docker", "tag", imageTag, backupImageTag); err != nil {
		backupImageTag = ""
	}

	var size int64
	if info, statErr := os.Stat(archivePath); statErr == nil {
		size = info.Size()
	}

	b := store.Backup{
		ID:           randomIDMain(),
		DeploymentID: dep.ID,
		Name:         "[" + trigger + "] " + time.Now().Format("2006-01-02 15:04:05"),
		Filename:     archiveName,
		SizeBytes:    size,
		ImageTag:     backupImageTag,
		CreatedBy:    "scheduler",
		CreatedAt:    time.Now(),
	}
	if err := st.AddBackup(b); err != nil {
		return "", err
	}
	return "Backup created: " + archiveName, nil
}

func randomIDMain() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// =====================================================================
// Helpers
// =====================================================================
func loadOrCreateSecret(dataDir string) ([]byte, error) {
	path := dataDir + "/session.secret"
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	secret, err := auth.NewSecret()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}

func methodGuard(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			middleware.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h(w, r)
	}
}

func methodSplit(byMethod map[string]http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := byMethod[r.Method]; ok {
			h(w, r)
			return
		}
		middleware.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
}

func serviceDispatch(h *handlers.ServiceHandlers) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "POST" && path == "/api/services/database":
			h.CreateDatabaseService(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/start"):
			h.Start(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/stop"):
			h.Stop(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/restart"):
			h.Restart(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/logs"):
			h.Logs(w, r)
		case r.Method == "DELETE":
			h.Delete(w, r)
		default:
			middleware.JSONError(w, http.StatusNotFound, "unknown route")
		}
	}
}

func dbDispatch(h *handlers.DatabaseHandlers) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "GET" && strings.HasSuffix(path, "/rows"):
			h.TableRows(w, r)
		case r.Method == "GET" && strings.HasSuffix(path, "/tables"):
			h.ListTables(w, r)
		case r.Method == "POST" && strings.HasSuffix(path, "/query"):
			h.RunQuery(w, r)
		case r.Method == "DELETE":
			h.DeleteConnection(w, r)
		default:
			middleware.JSONError(w, http.StatusNotFound, "unknown route")
		}
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}