// Package handlers — GitHub integration.
//
// Ce fichier gère la connexion du compte GitHub d'un utilisateur via
// un Personal Access Token (PAT). Une fois connecté, l'utilisateur peut :
//   - lister ses repos (publics + privés + organisations)
//   - importer un repo privé comme n'importe quel autre déploiement
//
// Le token est stocké en clair dans vpscontrol.json (permissions 0600
// root-only). Il n'est JAMAIS renvoyé par l'API une fois enregistré.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type GithubHandlers struct {
	Store      *store.Store
	DeployRoot string
}

// =====================================================================
// Helpers : appels à l'API GitHub
// =====================================================================

const githubAPIBase = "https://api.github.com"

// githubGet effectue un GET authentifié sur l'API GitHub et retourne
// le corps brut. Limite la réponse à 5 MB.
func githubGet(token, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", githubAPIBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "VPS-Control")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("GitHub token is invalid or expired")
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("GitHub token lacks required permissions (need 'repo' scope)")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub API error (HTTP %d): %s", resp.StatusCode, truncateError(string(body)))
	}
	return body, nil
}

// githubUser : réponse de /user
type githubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// fetchGithubUser : récupère le username + valide le token.
// Retourne aussi la liste des scopes lus depuis le header HTTP
// (non accessible via githubGet, on refait un appel dédié).
func fetchGithubUser(token string) (githubUser, []string, error) {
	req, err := http.NewRequest("GET", githubAPIBase+"/user", nil)
	if err != nil {
		return githubUser{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "VPS-Control")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return githubUser{}, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return githubUser{}, nil, errors.New("invalid GitHub token")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return githubUser{}, nil, fmt.Errorf("GitHub API error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var u githubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return githubUser{}, nil, err
	}

	// Header X-OAuth-Scopes contient "repo, read:org" etc.
	scopes := []string{}
	if s := resp.Header.Get("X-OAuth-Scopes"); s != "" {
		for _, sc := range strings.Split(s, ",") {
			sc = strings.TrimSpace(sc)
			if sc != "" {
				scopes = append(scopes, sc)
			}
		}
	}
	return u, scopes, nil
}

// githubRepo : représentation minimale d'un repo, telle qu'on la renvoie
// au frontend (ne pas exposer tous les champs GitHub inutiles).
type githubRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	UpdatedAt     string `json:"updated_at"`
}

// fetchGithubRepos : liste tous les repos accessibles avec ce token
// (personnels + organisations), en filtrant sur les permissions du
// token (le scope `repo` donne accès aux privés).
func fetchGithubRepos(token string) ([]githubRepo, error) {
	repos := []githubRepo{}
	// Pagination : max 3 pages de 100 (300 repos) — largement assez.
	for page := 1; page <= 3; page++ {
		path := fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=updated&affiliation=owner,collaborator,organization_member", page)
		body, err := githubGet(token, path)
		if err != nil {
			return nil, err
		}
		var batch []githubRepo
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("invalid GitHub response: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		repos = append(repos, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return repos, nil
}

// =====================================================================
// Routes
// =====================================================================

// Status : GET /api/github/status
// Renvoie l'état de connexion (sans jamais révéler le token).
func (h *GithubHandlers) Status(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	g, ok := h.Store.GetGithubToken(user.ID)
	if !ok {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"connected": false,
		})
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"connected":  true,
		"username":   g.Username,
		"scopes":     g.Scopes,
		"createdAt":  g.CreatedAt,
		"lastUsedAt": g.LastUsedAt,
	})
}

// SetToken : POST /api/github/token
// Body JSON : { "token": "ghp_..." }
func (h *GithubHandlers) SetToken(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		middleware.JSONError(w, http.StatusBadRequest, "token is required")
		return
	}
	// Un PAT GitHub classique commence par ghp_ / gho_ / ghu_ / ghs_ / ghr_
	// ou github_pat_ pour les fine-grained. On vérifie juste la longueur
	// minimale pour éviter les fautes de frappe évidentes.
	if len(req.Token) < 20 {
		middleware.JSONError(w, http.StatusBadRequest, "token looks too short")
		return
	}

	// Valider le token et récupérer le username + scopes.
	ghUser, scopes, err := fetchGithubUser(req.Token)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "GitHub rejected the token: "+err.Error())
		return
	}

	g := store.GithubToken{
		UserID:    user.ID,
		Token:     req.Token,
		Username:  ghUser.Login,
		Scopes:    scopes,
		CreatedAt: time.Now(),
	}
	if err := h.Store.SetGithubToken(g); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "github.connect",
		Target:    ghUser.Login,
	})

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"connected": true,
		"username":  ghUser.Login,
		"scopes":    scopes,
	})
}

// DeleteToken : DELETE /api/github/token
func (h *GithubHandlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err := h.Store.DeleteGithubToken(user.ID); err != nil {
		middleware.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "github.disconnect",
	})
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ListRepos : GET /api/github/repos
// Renvoie la liste des repos accessibles avec le token de l'utilisateur.
func (h *GithubHandlers) ListRepos(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	g, ok := h.Store.GetGithubToken(user.ID)
	if !ok {
		middleware.JSONError(w, http.StatusForbidden, "GitHub account not connected")
		return
	}

	repos, err := fetchGithubRepos(g.Token)
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = h.Store.TouchGithubToken(user.ID)
	middleware.JSON(w, http.StatusOK, repos)
}

// =====================================================================
// Import : clone un repo privé et déclenche le déploiement
// =====================================================================

// ImportRequest : POST /api/github/import
type githubImportRequest struct {
	Name          string `json:"name"`
	RepoFullName  string `json:"repoFullName"`  // ex : "user/monorepo"
	CloneURL      string `json:"cloneUrl"`      // optionnel : si absent, reconstruit depuis repoFullName
	Stack         string `json:"stack"`
	Port          string `json:"port"`
	NodeVersion   string `json:"nodeVersion"`
	PythonVersion string `json:"pythonVersion"`
	PHPVersion    string `json:"phpVersion"`
	AppSubdir     string `json:"appSubdir"`
}

// injectTokenInCloneURL : insère le token dans l'URL HTTPS du repo.
// https://github.com/user/repo.git  →  https://x-access-token:TOKEN@github.com/user/repo.git
func injectTokenInCloneURL(cloneURL, token string) (string, error) {
	u, err := url.Parse(cloneURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", errors.New("only HTTPS clone URLs are supported")
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String(), nil
}

// Import : clone un repo GitHub (privé ou public) avec le token de
// l'utilisateur, puis enchaîne sur le déploiement standard.
//
// On ne fait PAS de git clone ici : on délègue à DeployHandlers en lui
// passant l'URL avec token injecté. Comme ça toute la logique de
// détection de stack / build / run reste centralisée dans deploy.go.
func (h *GithubHandlers) Import(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	g, ok := h.Store.GetGithubToken(user.ID)
	if !ok {
		middleware.JSONError(w, http.StatusForbidden, "GitHub account not connected")
		return
	}

	var req githubImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.RepoFullName = strings.TrimSpace(req.RepoFullName)
	req.CloneURL = strings.TrimSpace(req.CloneURL)

	if !appNameRe.MatchString(req.Name) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid name (lowercase letters, digits, dashes, 2-40 characters)")
		return
	}
	if req.RepoFullName == "" && req.CloneURL == "" {
		middleware.JSONError(w, http.StatusBadRequest, "repoFullName or cloneUrl is required")
		return
	}

	// Reconstruire cloneURL si on a seulement repoFullName.
	// On ne fait JAMAIS confiance au cloneUrl fourni par le client :
	// on le valide contre github.com.
	if req.CloneURL == "" {
		req.CloneURL = "https://github.com/" + req.RepoFullName + ".git"
	}
	u, err := url.Parse(req.CloneURL)
	if err != nil || u.Host != "github.com" {
		middleware.JSONError(w, http.StatusBadRequest, "cloneUrl must point to github.com")
		return
	}
	// Nettoyer l'URL : retirer tout user:pass déjà présent (au cas où
	// le client aurait glissé un token dans l'URL).
	u.User = nil
	cleanCloneURL := u.String()

	// Injecter notre token.
	authURL, err := injectTokenInCloneURL(cleanCloneURL, g.Token)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Auto-port si non fourni.
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

	// Clone avec le token. On utilise --config pour ne pas laisser
	// le token traîner dans .git/config après le clone.
	out, err := runCommand(2*time.Minute, "git", "-c", "credential.helper=", "clone", "--depth", "1", authURL, targetDir)
	if err != nil {
		// Nettoyer le dossier partiel.
		_ = os.RemoveAll(targetDir)
		// Ne pas renvoyer le token dans l'erreur.
		safeOut := strings.ReplaceAll(out, g.Token, "***")
		safeOut = strings.ReplaceAll(safeOut, "x-access-token", "***")
		middleware.JSONError(w, http.StatusBadRequest, "git clone failed: "+truncateError(safeOut))
		return
	}

	// Supprimer les credentials éventuellement stockés (git les met dans
	// .git/config via l'URL si mal configuré — on s'assure qu'il n'y a
	// rien qui traîne).
	_, _ = runCommand(10*time.Second, "git", "-C", targetDir, "remote", "set-url", "origin", cleanCloneURL)

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
		_ = os.RemoveAll(targetDir)
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

	_ = h.Store.TouchGithubToken(user.ID)
	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "github.import",
		Target:    req.Name,
		Details:   map[string]interface{}{"repo": req.RepoFullName, "private": true},
	})

	// Déléguer le build + run au handler standard.
	dh := &DeployHandlers{Store: h.Store, DeployRoot: h.DeployRoot}
	dh.buildAndRun(w, req.Name, targetDir, effectiveDir, subdir, opts, "git", cleanCloneURL, user.ID)
}

// =====================================================================
// Validation du nom de repo (utilisé dans le formulaire)
// =====================================================================

// repoFullNameRe : "owner/repo", alphanumériques, tirets, underscores,
// points autorisés. Utilisé pour valider côté serveur.
var repoFullNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-_.]{0,38}/[a-zA-Z0-9][a-zA-Z0-9-_.]{0,99}$`)

// ValidateRepoFullName : exporté pour tests éventuels.
func ValidateRepoFullName(name string) bool {
	return repoFullNameRe.MatchString(name)
}