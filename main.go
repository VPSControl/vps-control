// VPS Control — a lightweight web admin panel for VPS instances, Pterodactyl-style.
//
// A single Go binary, no heavy runtime dependency (no Node/PHP for the panel
// itself), local JSON storage. Built to run comfortably on a 2GB RAM VPS
// alongside the deployed applications (which run in isolated Docker
// containers).
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"vpscontrol/internal/auth"
	"vpscontrol/internal/handlers"
	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

//go:embed web/index.html web/style.css web/app.js web/assets/*
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
	listenAddr := env("VPSCONTROL_LISTEN", "127.0.0.1:8090")
	srcDir := env("VPSCONTROL_SRC_DIR", "/opt/vpscontrol-src")
	repoURL := env("VPSCONTROL_REPO_URL", "https://github.com/Wazestudio/vps-control.git")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("could not create data directory %s: %v", dataDir, err)
	}
	if err := os.MkdirAll(deployRoot, 0o755); err != nil {
		log.Fatalf("could not create deploy directory %s: %v", deployRoot, err)
	}

	st, err := store.New(dataDir)
	if err != nil {
		log.Fatalf("storage error: %v", err)
	}

	secret, err := loadOrCreateSecret(dataDir)
	if err != nil {
		log.Fatalf("session secret error: %v", err)
	}
	webhookSecret, err := loadOrCreateWebhookSecret(dataDir)
	if err != nil {
		log.Fatalf("webhook secret error: %v", err)
	}

	authH := &handlers.AuthHandlers{Store: st, Secret: secret}
	userH := &handlers.UserHandlers{Store: st}
	fileH := &handlers.FileHandlers{Root: filesRoot}
	svcH := &handlers.ServiceHandlers{}
	deployH := &handlers.DeployHandlers{Store: st, DeployRoot: deployRoot}
	dbH := &handlers.DatabaseHandlers{Store: st}
	sysH := &handlers.SystemHandlers{Store: st, SrcDir: srcDir, RepoURL: repoURL, WebhookSecret: webhookSecret}

	mux := http.NewServeMux()

	// ---- Auth (public) ----
	mux.HandleFunc("/api/setup/status", methodGuard("GET", authH.SetupStatus))
	mux.HandleFunc("/api/setup", methodGuard("POST", authH.Setup))
	mux.HandleFunc("/api/login", methodGuard("POST", authH.Login))

	authMw := middleware.Auth(secret, st)
	adminMw := middleware.RequireAdmin

	// ---- Auth (authenticated) ----
	mux.Handle("/api/logout", authMw(http.HandlerFunc(authH.Logout)))
	mux.Handle("/api/me", authMw(http.HandlerFunc(authH.Me)))

	// ---- Users (admin) ----
	mux.Handle("/api/users", authMw(adminMw(methodSplit(map[string]http.HandlerFunc{
		"GET": userH.List, "POST": userH.Create,
	}))))
	mux.Handle("/api/users/", authMw(adminMw(http.HandlerFunc(userH.Delete))))

	// ---- Files (authenticated) ----
	mux.Handle("/api/files", authMw(methodSplit(map[string]http.HandlerFunc{
		"GET": fileH.List, "DELETE": fileH.Delete,
	})))
	mux.Handle("/api/files/content", authMw(methodSplit(map[string]http.HandlerFunc{
		"GET": fileH.ReadContent, "PUT": fileH.WriteContent,
	})))
	mux.Handle("/api/files/mkdir", authMw(http.HandlerFunc(fileH.Mkdir)))
	mux.Handle("/api/files/upload", authMw(http.HandlerFunc(fileH.Upload)))
	mux.Handle("/api/files/download", authMw(http.HandlerFunc(fileH.Download)))

	// ---- Docker services (authenticated) ----
	mux.Handle("/api/services", authMw(http.HandlerFunc(svcH.List)))
	mux.Handle("/api/services/", authMw(http.HandlerFunc(serviceDispatch(svcH))))

	// ---- Deployments (authenticated) ----
	mux.Handle("/api/deploy/git", authMw(http.HandlerFunc(deployH.DeployGit)))
	mux.Handle("/api/deploy/upload", authMw(http.HandlerFunc(deployH.DeployUpload)))
	mux.Handle("/api/deployments", authMw(http.HandlerFunc(deployH.List)))
	mux.Handle("/api/deployments/", authMw(http.HandlerFunc(deploymentDispatch(deployH))))

	// ---- Databases (authenticated) ----
	mux.Handle("/api/db/connections", authMw(methodSplit(map[string]http.HandlerFunc{
		"GET": dbH.ListConnections, "POST": dbH.AddConnection,
	})))
	mux.Handle("/api/db/connections/", authMw(http.HandlerFunc(dbDispatch(dbH))))

	// ---- System (authenticated / admin for sensitive actions) ----
	mux.Handle("/api/system/info", authMw(http.HandlerFunc(sysH.Info)))
	mux.Handle("/api/system/stats", authMw(http.HandlerFunc(sysH.Stats)))
	mux.Handle("/api/system/check-update", authMw(adminMw(http.HandlerFunc(sysH.CheckUpdate))))
	mux.Handle("/api/system/update", authMw(adminMw(http.HandlerFunc(sysH.Update))))
	mux.Handle("/api/system/webhook", authMw(adminMw(http.HandlerFunc(sysH.WebhookInfo))))

	// ---- GitHub webhook (public: authenticated by HMAC signature, not by session) ----
	mux.HandleFunc("/api/webhook/update", methodGuard("POST", sysH.Webhook))

	// ---- Static frontend ----
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      logRequests(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // docker builds can take a while
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("VPS Control listening on %s (data: %s, apps: %s, files: %s)", listenAddr, dataDir, deployRoot, filesRoot)
	log.Fatal(srv.ListenAndServe())
}

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

// loadOrCreateWebhookSecret is separate from the session secret: it's meant
// to be copied out and pasted into GitHub's webhook settings, so it gets its
// own file and its own lifecycle (rotating the session secret shouldn't log
// out every webhook integration, and vice versa).
func loadOrCreateWebhookSecret(dataDir string) ([]byte, error) {
	path := dataDir + "/webhook.secret"
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

// serviceDispatch routes everything under /api/services/...:
// POST .../database, POST .../{name}/start|stop|restart, GET .../{name}/logs, DELETE .../{name}
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

func deploymentDispatch(h *handlers.DeployHandlers) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "POST" && strings.HasSuffix(path, "/redeploy"):
			h.Redeploy(w, r)
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
