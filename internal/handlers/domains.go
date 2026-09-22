package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// DomainHandlers gère les domaines personnalisés d'une app : génération du
// vhost Nginx, activation, et obtention/renouvellement du certificat TLS
// via Certbot. Tout se fait directement sur l'hôte (le panel tourne déjà
// en root pour piloter Docker, cf. README).
type DomainHandlers struct {
	Store          *store.Store
	SitesAvailable string // défaut : /etc/nginx/sites-available
	SitesEnabled   string // défaut : /etc/nginx/sites-enabled
}

func (h *DomainHandlers) availDir() string {
	if h.SitesAvailable != "" {
		return h.SitesAvailable
	}
	return "/etc/nginx/sites-available"
}

func (h *DomainHandlers) enabledDir() string {
	if h.SitesEnabled != "" {
		return h.SitesEnabled
	}
	return "/etc/nginx/sites-enabled"
}

// Un nom de domaine "raisonnable" : au moins un point, labels alphanumériques
// (tirets autorisés au milieu), TLD alphabétique. Volontairement strict :
// c'est un nom qui finit dans un fichier de config Nginx.
var validHostnameRe = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
var slugRe = regexp.MustCompile(`[^a-z0-9.-]+`)

func safeSlug(hostname string) string {
	return slugRe.ReplaceAllString(strings.ToLower(hostname), "-")
}

// validTargetPort : le domaine ne peut pointer que vers un port déjà
// alloué à CETTE app (le port par défaut ou une allocation) — pas
// n'importe quel port du serveur.
func validTargetPort(dep store.Deployment, port string) bool {
	if port != "" && port == dep.Port {
		return true
	}
	for _, a := range dep.Allocations {
		if a.Port == port {
			return true
		}
	}
	return false
}

func buildDomainNginxConf(hostname, port string) string {
	return fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location / {
        proxy_pass http://127.0.0.1:%s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
`, hostname, port)
}

func nginxTestAndReload() error {
	if out, err := runCommand(15*time.Second, "nginx", "-t"); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(out))
	}
	if out, err := runCommand(15*time.Second, "systemctl", "reload", "nginx"); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(out))
	}
	return nil
}

func ensureCertbotInstalled() error {
	if _, err := exec.LookPath("certbot"); err == nil {
		return nil
	}
	if out, err := runCommand(3*time.Minute, "apt-get", "install", "-y", "certbot", "python3-certbot-nginx"); err != nil {
		return fmt.Errorf("could not install certbot: %s", strings.TrimSpace(out))
	}
	if _, err := exec.LookPath("certbot"); err != nil {
		return fmt.Errorf("certbot installation did not complete")
	}
	return nil
}

// certbotErrorSummary : Certbot est bavard, on ne garde que la dernière
// ligne non vide (généralement la plus parlante) pour l'UI.
func certbotErrorSummary(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return "certbot failed"
	}
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	return "certbot failed"
}

// issueCertificate lance Certbot pour le domaine et met à jour son statut
// SSL en conséquence. Le vhost HTTP doit déjà exister et être actif — le
// plugin `--nginx` de Certbot le complète lui-même avec le bloc 443.
func (h *DomainHandlers) issueCertificate(dom store.Domain, email string) (store.Domain, error) {
	if err := ensureCertbotInstalled(); err != nil {
		dom.SSLStatus = "error"
		dom.SSLError = err.Error()
		return dom, err
	}

	args := []string{"--nginx", "-d", dom.Hostname, "--redirect", "--agree-tos", "--non-interactive"}
	if email != "" {
		args = append(args, "-m", email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}

	out, err := runCommand(2*time.Minute, "certbot", args...)
	if err != nil {
		dom.SSLStatus = "error"
		dom.SSLError = certbotErrorSummary(out)
		return dom, fmt.Errorf("%s", dom.SSLError)
	}

	if rerr := nginxTestAndReload(); rerr != nil {
		dom.SSLStatus = "error"
		dom.SSLError = rerr.Error()
		return dom, rerr
	}

	dom.SSLStatus = "active"
	dom.SSLError = ""
	dom.ForceHTTPS = true
	return dom, nil
}

func domainIDFromPath(path, depID string) string {
	trimmed := strings.TrimPrefix(path, "/api/deployments/"+depID+"/domains/")
	trimmed = strings.TrimSuffix(trimmed, "/ssl")
	return strings.Trim(trimmed, "/")
}

// List renvoie les domaines rattachés à l'app.
func (h *DomainHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	middleware.JSON(w, http.StatusOK, h.Store.ListDomainsForDeployment(dep.ID))
}

// Create : écrit le vhost Nginx, l'active, recharge Nginx, et — si demandé —
// obtient tout de suite un certificat Let's Encrypt.
func (h *DomainHandlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req struct {
		Hostname string `json:"hostname"`
		Port     string `json:"port"`
		SSL      bool   `json:"ssl"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	hostname := strings.ToLower(strings.TrimSpace(req.Hostname))
	if !validHostnameRe.MatchString(hostname) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid domain name")
		return
	}
	if _, exists := h.Store.FindDomainByHostname(hostname); exists {
		middleware.JSONError(w, http.StatusConflict, "this domain is already configured")
		return
	}

	port := strings.TrimSpace(req.Port)
	if port == "" {
		port = dep.Port
		for _, a := range dep.Allocations {
			if a.Primary {
				port = a.Port
			}
		}
	}
	if !validTargetPort(dep, port) {
		middleware.JSONError(w, http.StatusBadRequest, "port "+port+" is not allocated to this app")
		return
	}

	confName := "vpscontrol-domain-" + safeSlug(hostname) + ".conf"
	availPath := filepath.Join(h.availDir(), confName)
	enabledPath := filepath.Join(h.enabledDir(), confName)

	if err := os.WriteFile(availPath, []byte(buildDomainNginxConf(hostname, port)), 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "could not write nginx config: "+err.Error())
		return
	}
	if err := os.Symlink(availPath, enabledPath); err != nil && !os.IsExist(err) {
		_ = os.Remove(availPath)
		middleware.JSONError(w, http.StatusInternalServerError, "could not enable nginx site: "+err.Error())
		return
	}
	if err := nginxTestAndReload(); err != nil {
		_ = os.Remove(enabledPath)
		_ = os.Remove(availPath)
		middleware.JSONError(w, http.StatusBadGateway, "nginx rejected the new config: "+err.Error())
		return
	}

	dom := store.Domain{
		ID:           randomID(),
		DeploymentID: dep.ID,
		Hostname:     hostname,
		TargetPort:   port,
		ConfigPath:   availPath,
		SSLStatus:    "none",
		CreatedAt:    time.Now(),
	}

	var warning string
	if req.SSL {
		updated, sslErr := h.issueCertificate(dom, strings.TrimSpace(req.Email))
		dom = updated
		if sslErr != nil {
			warning = "domain added, but the SSL certificate failed: " + sslErr.Error()
		}
	}

	if err := h.Store.AddDomain(dom); err != nil {
		_ = os.Remove(enabledPath)
		_ = os.Remove(availPath)
		_ = nginxTestAndReload()
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "domain.create",
		Target:    dep.Name,
		Details:   map[string]interface{}{"hostname": hostname, "port": port, "ssl": req.SSL},
	})

	if warning != "" {
		middleware.JSON(w, http.StatusCreated, map[string]interface{}{"domain": dom, "warning": warning})
		return
	}
	middleware.JSON(w, http.StatusCreated, dom)
}

// IssueSSL : (ré)essaie d'obtenir/renouveler le certificat d'un domaine
// déjà configuré (utile si la première tentative avait échoué, ou pour
// forcer un renouvellement).
func (h *DomainHandlers) IssueSSL(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	id := domainIDFromPath(r.URL.Path, dep.ID)
	dom, found := h.Store.FindDomainByID(id)
	if !found || dom.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "domain not found")
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	updated, err := h.issueCertificate(dom, strings.TrimSpace(req.Email))
	_ = h.Store.UpdateDomain(updated)

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "domain.ssl",
		Target:    dep.Name,
		Details:   map[string]interface{}{"hostname": dom.Hostname, "success": err == nil},
	})

	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, updated)
}

// Delete : retire le vhost Nginx (et le certificat s'il y en avait un),
// recharge Nginx, puis oublie le domaine.
func (h *DomainHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	id := domainIDFromPath(r.URL.Path, dep.ID)
	dom, found := h.Store.FindDomainByID(id)
	if !found || dom.DeploymentID != dep.ID {
		middleware.JSONError(w, http.StatusNotFound, "domain not found")
		return
	}

	confName := filepath.Base(dom.ConfigPath)
	if confName == "" || confName == "." || confName == string(filepath.Separator) {
		confName = "vpscontrol-domain-" + safeSlug(dom.Hostname) + ".conf"
	}
	_ = os.Remove(filepath.Join(h.enabledDir(), confName))
	_ = os.Remove(filepath.Join(h.availDir(), confName))
	_ = nginxTestAndReload()

	if dom.SSLStatus == "active" {
		_, _ = runCommand(30*time.Second, "certbot", "delete", "--cert-name", dom.Hostname, "--non-interactive")
	}

	if err := h.Store.DeleteDomain(dom.ID); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "domain.delete",
		Target:    dep.Name,
		Details:   map[string]interface{}{"hostname": dom.Hostname},
	})
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
