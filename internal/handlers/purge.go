// Package handlers — Suppression en cascade.
//
// Ce fichier regroupe la logique de nettoyage des ressources liées à un
// déploiement ou à un utilisateur : conteneurs Docker, images, dossiers
// sur disque, backups, vhosts Nginx, certificats SSL.
//
// Toutes les fonctions sont en best-effort : elles collectent les erreurs
// mais continuent le nettoyage, afin qu'une ressource bloquée (ex :
// conteneur Docker en cours de kill) ne laisse pas le reste en plan.
package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vpscontrol/internal/store"
)

// PurgeResult résume ce qui a été supprimé lors d'un purge.
type PurgeResult struct {
	ContainersRemoved int      `json:"containersRemoved"`
	ImagesRemoved     int      `json:"imagesRemoved"`
	BackupsRemoved    int      `json:"backupsRemoved"`
	DomainsRemoved    int      `json:"domainsRemoved"`
	FoldersRemoved    int      `json:"foldersRemoved"`
	DeploymentsPurged int      `json:"deploymentsPurged"`
	ServersPurged     int      `json:"serversPurged"`
	Errors            []string `json:"errors,omitempty"`
}

// mergePurge : fusionne b dans a (utilisé pour agréger les résultats).
func mergePurge(a *PurgeResult, b PurgeResult) {
	a.ContainersRemoved += b.ContainersRemoved
	a.ImagesRemoved += b.ImagesRemoved
	a.BackupsRemoved += b.BackupsRemoved
	a.DomainsRemoved += b.DomainsRemoved
	a.FoldersRemoved += b.FoldersRemoved
	a.DeploymentsPurged += b.DeploymentsPurged
	a.ServersPurged += b.ServersPurged
	a.Errors = append(a.Errors, b.Errors...)
}

// =====================================================================
// Purge d'un déploiement
// =====================================================================

// purgeDeployment supprime toutes les ressources associées à un
// déploiement : conteneur Docker, image, backups, vhosts Nginx,
// dossier sur disque, et l'entrée dans le store.
//
// IMPORTANT : ne pas appeler sans vérifier au préalable que le
// déploiement existe bien dans le store et que l'appelant y est autorisé.
func (h *ServerHandlers) purgeDeployment(dep store.Deployment) PurgeResult {
	res := PurgeResult{}

	// 1. Supprimer les vhosts Nginx + certificats SSL liés à ce déploiement.
	domains := h.Store.ListDomainsForDeployment(dep.ID)
	for _, dom := range domains {
		if err := removeDomainArtifacts(dom); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("domain %s: %v", dom.Hostname, err))
		} else {
			res.DomainsRemoved++
		}
		_ = h.Store.DeleteDomain(dom.ID)
	}

	// 2. Supprimer les backups (fichiers .tar.gz + images taguées).
	backups := h.Store.ListBackupsForDeployment(dep.ID)
	for _, b := range backups {
		// Fichier archive
		archivePath := filepath.Join("/opt/vpscontrol/backups", b.Filename)
		if err := removeFileIfExists(archivePath); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("backup file %s: %v", b.Filename, err))
		}
		// Image Docker du backup
		if b.ImageTag != "" {
			if _, err := runCommand(20*time.Second, "docker", "rmi", b.ImageTag); err == nil {
				res.ImagesRemoved++
			}
		}
		// Entrée store
		_ = h.Store.DeleteBackup(b.ID)
		res.BackupsRemoved++
	}

	// 3. Supprimer le conteneur Docker.
	if dep.Container != "" {
		if _, err := runCommand(30*time.Second, "docker", "rm", "-f", dep.Container); err == nil {
			res.ContainersRemoved++
		}
	}

	// 4. Supprimer l'image Docker principale.
	imageTag := "vpscontrol-" + dep.Name
	if _, err := runCommand(20*time.Second, "docker", "rmi", imageTag); err == nil {
		res.ImagesRemoved++
	}

	// 5. Supprimer le dossier sur disque.
	if dep.Path != "" {
		if err := removeAllSafe(dep.Path); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("folder %s: %v", dep.Path, err))
		} else {
			res.FoldersRemoved++
		}
	}

	// 6. Supprimer l'entrée dans le store (cascade déjà sur subusers,
	// invitations, backups et schedules dans DeleteDeployment).
	if err := h.Store.DeleteDeployment(dep.ID); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("store delete %s: %v", dep.Name, err))
	} else {
		res.DeploymentsPurged++
	}

	return res
}

// =====================================================================
// Purge d'un serveur (utilisé par DeleteServer)
// =====================================================================

// purgeServer supprime toutes les apps d'un serveur (via purgeDeployment)
// puis le serveur lui-même. Retourne le résultat agrégé.
//
// IMPORTANT : ne pas appeler sans vérifier que l'appelant est autorisé.
func (h *ServerHandlers) purgeServer(srv store.Server) PurgeResult {
	res := PurgeResult{}
	deps := h.Store.ListDeploymentsInServer(srv.ID)
	for _, d := range deps {
		mergePurge(&res, h.purgeDeployment(d))
	}
	if err := h.Store.DeleteServer(srv.ID); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("server %s: %v", srv.Name, err))
	} else {
		res.ServersPurged++
	}
	return res
}

// =====================================================================
// Purge d'un utilisateur
// =====================================================================

// purgeUserResources supprime tout ce qui appartient à un utilisateur :
//   - ses serveurs et les apps de ces serveurs (cascade)
//   - ses apps orphelines éventuelles (au cas où, ceinture + bretelles)
//   - son token GitHub
//   - ses tokens API
//   - ses subuser entries (où il apparaît comme collaborateur)
//   - ses invitations en tant que créateur
//
// Ne supprime PAS le user du store : c'est le rôle de l'appelant
// (après avoir vérifié les règles métier, comme "ne pas supprimer
// le dernier admin").
func (h *ServerHandlers) purgeUserResources(userID string) PurgeResult {
	res := PurgeResult{}

	// 1. Purger les serveurs possédés par cet utilisateur.
	servers := h.Store.ListServersForUser(userID)
	for _, srv := range servers {
		mergePurge(&res, h.purgeServer(srv))
	}

	// 2. Purger les apps orphelines (au cas où une app aurait perdu son
	// ServerID suite à un bug ou une migration incomplète).
	orphans := h.Store.ListDeploymentsOwnedBy(userID)
	for _, d := range orphans {
		// Si le ServerID n'est plus valide, on purge quand même.
		if d.ServerID == "" {
			mergePurge(&res, h.purgeDeployment(d))
		} else if _, ok := h.Store.FindServerByID(d.ServerID); !ok {
			mergePurge(&res, h.purgeDeployment(d))
		}
	}

	// 3. Token GitHub.
	if err := h.Store.DeleteGithubToken(userID); err == nil {
		// pas de compteur dédié, on l'ajoute aux "folders" pour l'info
	}

	// 4. API tokens : on les révoque plutôt que de les supprimer, pour
	// garder une trace. Mais comme on va supprimer le user, autant les
	// purger complètement pour ne pas laisser d'entrées orphelines.
	// (Le store les supprime déjà dans DeleteUser, donc on ne fait rien ici.)

	// 5. Retirer cet utilisateur de tous ses rôles de subuser sur des
	// apps d'autres users (il ne doit plus apparaître dans les listes
	// de collaborateurs).
	// NB : DeleteUser fait déjà ça, mais on le refait ici pour être
	// cohérent si on appelle purgeUserResources sans DeleteUser.
	for _, dep := range h.Store.ListDeployments() {
		subs := h.Store.ListSubusersForDeployment(dep.ID)
		for _, sub := range subs {
			if sub.UserID == userID {
				_ = h.Store.DeleteSubuser(sub.ID)
			}
		}
	}

	return res
}

// =====================================================================
// Helpers de nettoyage bas niveau
// =====================================================================

// removeFileIfExists : supprime un fichier s'il existe. Renvoie nil si
// le fichier est déjà absent.
func removeFileIfExists(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

// removeDomainArtifacts : supprime le vhost Nginx, son symlink, et
// (best-effort) le certificat Let's Encrypt associé.
//
// On refait ici la logique de DomainHandlers.Delete pour pouvoir purger
// un domaine sans passer par un handler HTTP.
func removeDomainArtifacts(dom store.Domain) error {
	if dom.Hostname == "" {
		return nil
	}

	// Nom de fichier standard utilisé par DomainHandlers.Create.
	confName := "vpscontrol-domain-" + safeSlug(dom.Hostname) + ".conf"

	// Suppression du symlink + du fichier.
	_ = os.Remove(filepath.Join("/etc/nginx/sites-enabled", confName))
	_ = os.Remove(filepath.Join("/etc/nginx/sites-available", confName))

	// Si un chemin explicite était stocké, on le supprime aussi.
	if dom.ConfigPath != "" {
		_ = os.Remove(dom.ConfigPath)
	}

	// Nginx reload (best-effort, on ne veut pas faire échouer le purge
	// si Nginx est déjà down).
	_, _ = runCommand(15*time.Second, "systemctl", "reload", "nginx")

	// Certificat SSL : si Let's Encrypt en a un pour ce hostname, on
	// le supprime. Certbot utilise le hostname comme --cert-name par
	// défaut dans notre implémentation.
	if dom.SSLStatus == "active" {
		_, _ = runCommand(30*time.Second, "certbot", "delete",
			"--cert-name", dom.Hostname, "--non-interactive")
	}

	return nil
}

// =====================================================================
// Preview (avant suppression)
// =====================================================================

// UserPurgePreview résume ce qui serait supprimé pour un user, pour
// affichage dans le dialog de confirmation admin.
type UserPurgePreview struct {
	UserID        string `json:"userId"`
	Username      string `json:"username"`
	ServersCount  int    `json:"serversCount"`
	AppsCount     int    `json:"appsCount"`
	BackupsCount  int    `json:"backupsCount"`
	DomainsCount  int    `json:"domainsCount"`
	DiskUsedMB    int64  `json:"diskUsedMB"`
}

// buildUserPurgePreview : scanne les ressources du user pour préparer
// le dialog de confirmation. Ne modifie rien.
func (h *ServerHandlers) buildUserPurgePreview(user store.User) UserPurgePreview {
	preview := UserPurgePreview{
		UserID:   user.ID,
		Username: user.Username,
	}

	servers := h.Store.ListServersForUser(user.ID)
	preview.ServersCount = len(servers)

	// Compter les apps : celles dans les serveurs + orphelines.
	seen := map[string]bool{}
	for _, srv := range servers {
		deps := h.Store.ListDeploymentsInServer(srv.ID)
		for _, d := range deps {
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			preview.AppsCount++
			preview.DiskUsedMB += dirSize(d.Path) / (1024 * 1024)
			preview.BackupsCount += len(h.Store.ListBackupsForDeployment(d.ID))
			preview.DomainsCount += len(h.Store.ListDomainsForDeployment(d.ID))
		}
	}
	// Apps orphelines.
	for _, d := range h.Store.ListDeploymentsOwnedBy(user.ID) {
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		preview.AppsCount++
		preview.DiskUsedMB += dirSize(d.Path) / (1024 * 1024)
		preview.BackupsCount += len(h.Store.ListBackupsForDeployment(d.ID))
		preview.DomainsCount += len(h.Store.ListDomainsForDeployment(d.ID))
	}

	return preview
}

// =====================================================================
// Utilitaire : slug de hostname (doit rester identique à domains.go)
// =====================================================================

// safeSlug : reproduit la fonction de domains.go pour générer le même
// nom de fichier de config Nginx.
func safeSlug(hostname string) string {
	// Copie exacte de domains.go — NE PAS MODIFIER sans mettre à jour
	// domains.go en parallèle, sinon les vhosts ne seront pas supprimés.
	var b strings.Builder
	for _, r := range strings.ToLower(hostname) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}