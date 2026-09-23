package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	Salt         string    `json:"salt"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
}

type DBConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Allocation struct {
	ID        string    `json:"id"`
	Port      string    `json:"port"`
	Label     string    `json:"label,omitempty"`
	Primary   bool      `json:"primary"`
	CreatedAt time.Time `json:"createdAt"`
}

// Domain represents a custom hostname pointed at one of an app's
// allocations, reverse-proxied through the host's Nginx and optionally
// covered by a Let's Encrypt certificate (Certbot).
//
// SSLStatus possibles :
//   - "none"    → HTTP uniquement, pas de certificat demandé
//   - "pending" → demande de certificat en cours
//   - "active"  → certificat valide, HTTPS actif
//   - "error"   → dernière tentative de certificat en échec (voir SSLError)
type Domain struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Hostname     string    `json:"hostname"`
	TargetPort   string    `json:"targetPort"`
	ConfigPath   string    `json:"configPath,omitempty"`
	SSLStatus    string    `json:"sslStatus"`
	SSLError     string    `json:"sslError,omitempty"`
	ForceHTTPS   bool      `json:"forceHttps"`
	CreatedAt    time.Time `json:"createdAt"`
}

type LinkedDB struct {
	ID           string    `json:"id"`
	Engine       string    `json:"engine"`
	Container    string    `json:"container"`
	Host         string    `json:"host"`
	Port         string    `json:"port"`
	DatabaseName string    `json:"databaseName"`
	Username     string    `json:"username"`
	Password     string    `json:"password"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Limits struct {
	CPUQuota  float64 `json:"cpuQuota"`
	MemoryMB  int     `json:"memoryMB"`
	DiskMB    int     `json:"diskMB"`
	IOWeight  int     `json:"ioWeight"`
	PidsLimit int     `json:"pidsLimit"`
}

// Deployment Status possibles :
//   - "draft"      → fichiers importés, pas encore buildé
//   - "deploying"  → build en cours
//   - "running"    → conteneur démarré
//   - "stopped"    → conteneur arrêté manuellement
//   - "error"      → le conteneur a crashé (dernière erreur dans LastError)
type Deployment struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Stack         string       `json:"stack"`
	SourceType    string       `json:"sourceType"`
	SourceRef     string       `json:"sourceRef"`
	Path          string       `json:"path"`
	AppSubdir     string       `json:"appSubdir,omitempty"` // sous-dossier contenant le projet (vide = racine)
	Port          string       `json:"port"`
	Container     string       `json:"container"`
	NodeVersion   string       `json:"nodeVersion,omitempty"`
	PythonVersion string       `json:"pythonVersion,omitempty"`
	PHPVersion    string       `json:"phpVersion,omitempty"`
	OwnerID       string       `json:"ownerId"`
	Status        string       `json:"status"`
	LastError     string       `json:"lastError,omitempty"`
	LastErrorAt   time.Time    `json:"lastErrorAt,omitempty"`
	EnvVars       []EnvVar     `json:"envVars,omitempty"`
	Allocations   []Allocation `json:"allocations,omitempty"`
	LinkedDBs     []LinkedDB   `json:"linkedDBs,omitempty"`
	Limits        Limits       `json:"limits,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
}

type Subuser struct {
	ID           string    `json:"id"`
	UserID       string    `json:"userId"`
	DeploymentID string    `json:"deploymentId"`
	Permissions  []string  `json:"permissions"`
	InvitedBy    string    `json:"invitedBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Invitation struct {
	Token        string    `json:"token"`
	DeploymentID string    `json:"deploymentId"`
	Permissions  []string  `json:"permissions"`
	CreatedBy    string    `json:"createdBy"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Used         bool      `json:"used"`
}

type Backup struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Name         string    `json:"name"`
	Filename     string    `json:"filename"`
	SizeBytes    int64     `json:"sizeBytes"`
	ImageTag     string    `json:"imageTag,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	Notes        string    `json:"notes,omitempty"`
}

type Schedule struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Name         string    `json:"name"`
	CronExpr     string    `json:"cronExpr"`
	Action       string    `json:"action"`
	Command      string    `json:"command,omitempty"`
	Enabled      bool      `json:"enabled"`
	LastRunAt    time.Time `json:"lastRunAt,omitempty"`
	LastStatus   string    `json:"lastStatus,omitempty"`
	LastOutput   string    `json:"lastOutput,omitempty"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

type ActivityEntry struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	ActorID   string                 `json:"actorId"`
	ActorName string                 `json:"actorName"`
	Action    string                 `json:"action"`
	Target    string                 `json:"target,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
	IP        string                 `json:"ip,omitempty"`
}

type APIToken struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Revoked   bool      `json:"revoked"`
}

type Notification struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId,omitempty"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Level     string    `json:"level"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"createdAt"`
}

type data struct {
	Users         []User          `json:"users"`
	DBConns       []DBConnection  `json:"dbConnections"`
	Deployments   []Deployment    `json:"deployments"`
	Subusers      []Subuser       `json:"subusers,omitempty"`
	Invitations   []Invitation    `json:"invitations,omitempty"`
	Backups       []Backup        `json:"backups,omitempty"`
	Schedules     []Schedule      `json:"schedules,omitempty"`
	Activities    []ActivityEntry `json:"activities,omitempty"`
	APITokens     []APIToken      `json:"apiTokens,omitempty"`
	Notifications []Notification  `json:"notifications,omitempty"`
	Domains       []Domain        `json:"domains,omitempty"`
	WebhookSecret string          `json:"webhookSecret,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	d    data
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "vpscontrol.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.d = data{}
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		s.d = data{}
		return nil
	}
	return json.Unmarshal(b, &s.d)
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---- Users ----

func (s *Store) ListUsers() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, len(s.d.Users))
	copy(out, s.d.Users)
	return out
}

func (s *Store) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.d.Users)
}

func (s *Store) FindUserByUsername(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.d.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func (s *Store) FindUserByID(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.d.Users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

func (s *Store) AddUser(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Users {
		if existing.Username == u.Username {
			return errors.New("this username already exists")
		}
	}
	s.d.Users = append(s.d.Users, u)
	return s.saveLocked()
}

func (s *Store) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	adminCount := 0
	idx := -1
	for i, u := range s.d.Users {
		if u.Role == "admin" {
			adminCount++
		}
		if u.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("user not found")
	}
	if s.d.Users[idx].Role == "admin" && adminCount <= 1 {
		return errors.New("cannot delete the last remaining admin account")
	}
	s.d.Users = append(s.d.Users[:idx], s.d.Users[idx+1:]...)

	var subKeep []Subuser
	for _, sub := range s.d.Subusers {
		if sub.UserID != id {
			subKeep = append(subKeep, sub)
		}
	}
	s.d.Subusers = subKeep

	var tokKeep []APIToken
	for _, t := range s.d.APITokens {
		if t.UserID != id {
			tokKeep = append(tokKeep, t)
		}
	}
	s.d.APITokens = tokKeep

	return s.saveLocked()
}

// ---- DB connections ----

func (s *Store) ListDBConnections() []DBConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DBConnection, len(s.d.DBConns))
	copy(out, s.d.DBConns)
	return out
}

func (s *Store) FindDBConnection(id string) (DBConnection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.d.DBConns {
		if c.ID == id {
			return c, true
		}
	}
	return DBConnection{}, false
}

func (s *Store) AddDBConnection(c DBConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.DBConns = append(s.d.DBConns, c)
	return s.saveLocked()
}

func (s *Store) DeleteDBConnection(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, c := range s.d.DBConns {
		if c.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("connection not found")
	}
	s.d.DBConns = append(s.d.DBConns[:idx], s.d.DBConns[idx+1:]...)
	return s.saveLocked()
}

// ---- Deployments ----

func (s *Store) ListDeployments() []Deployment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Deployment, len(s.d.Deployments))
	copy(out, s.d.Deployments)
	return out
}

func (s *Store) ListDeploymentsForUser(userID, role string) []Deployment {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if role == "admin" {
		out := make([]Deployment, len(s.d.Deployments))
		copy(out, s.d.Deployments)
		return out
	}

	access := map[string]bool{}
	for _, sub := range s.d.Subusers {
		if sub.UserID == userID {
			access[sub.DeploymentID] = true
		}
	}

	out := make([]Deployment, 0)
	for _, d := range s.d.Deployments {
		if d.OwnerID == userID || access[d.ID] {
			out = append(out, d)
		}
	}
	return out
}

func (s *Store) FindDeploymentByID(id string) (Deployment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Deployments {
		if d.ID == id {
			return d, true
		}
	}
	return Deployment{}, false
}

func (s *Store) FindDeploymentByName(name string) (Deployment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Deployments {
		if d.Name == name {
			return d, true
		}
	}
	return Deployment{}, false
}

func (s *Store) AddDeployment(dep Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Deployments {
		if existing.Name == dep.Name {
			return errors.New("a deployment with this name already exists")
		}
	}
	s.d.Deployments = append(s.d.Deployments, dep)
	return s.saveLocked()
}

func (s *Store) UpdateDeployment(dep Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.d.Deployments {
		if d.ID == dep.ID {
			s.d.Deployments[i] = dep
			return s.saveLocked()
		}
	}
	return errors.New("deployment not found")
}

func (s *Store) DeleteDeployment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.d.Deployments {
		if d.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("deployment not found")
	}
	s.d.Deployments = append(s.d.Deployments[:idx], s.d.Deployments[idx+1:]...)

	var subKeep []Subuser
	for _, sub := range s.d.Subusers {
		if sub.DeploymentID != id {
			subKeep = append(subKeep, sub)
		}
	}
	s.d.Subusers = subKeep

	var invKeep []Invitation
	for _, inv := range s.d.Invitations {
		if inv.DeploymentID != id {
			invKeep = append(invKeep, inv)
		}
	}
	s.d.Invitations = invKeep

	var bkpKeep []Backup
	for _, b := range s.d.Backups {
		if b.DeploymentID != id {
			bkpKeep = append(bkpKeep, b)
		}
	}
	s.d.Backups = bkpKeep

	var schKeep []Schedule
	for _, sc := range s.d.Schedules {
		if sc.DeploymentID != id {
			schKeep = append(schKeep, sc)
		}
	}
	s.d.Schedules = schKeep

	return s.saveLocked()
}

// ---- Subusers ----

func (s *Store) ListSubusersForDeployment(deploymentID string) []Subuser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Subuser{}
	for _, sub := range s.d.Subusers {
		if sub.DeploymentID == deploymentID {
			out = append(out, sub)
		}
	}
	return out
}

func (s *Store) FindSubuser(userID, deploymentID string) (Subuser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.d.Subusers {
		if sub.UserID == userID && sub.DeploymentID == deploymentID {
			return sub, true
		}
	}
	return Subuser{}, false
}

func (s *Store) FindSubuserByID(id string) (Subuser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.d.Subusers {
		if sub.ID == id {
			return sub, true
		}
	}
	return Subuser{}, false
}

func (s *Store) AddSubuser(sub Subuser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Subusers {
		if existing.UserID == sub.UserID && existing.DeploymentID == sub.DeploymentID {
			return errors.New("this user is already a subuser of this deployment")
		}
	}
	s.d.Subusers = append(s.d.Subusers, sub)
	return s.saveLocked()
}

func (s *Store) UpdateSubuserPermissions(id string, perms []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sub := range s.d.Subusers {
		if sub.ID == id {
			s.d.Subusers[i].Permissions = perms
			return s.saveLocked()
		}
	}
	return errors.New("subuser not found")
}

func (s *Store) DeleteSubuser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, sub := range s.d.Subusers {
		if sub.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("subuser not found")
	}
	s.d.Subusers = append(s.d.Subusers[:idx], s.d.Subusers[idx+1:]...)
	return s.saveLocked()
}

// ---- Invitations ----

func (s *Store) AddInvitation(inv Invitation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Invitations = append(s.d.Invitations, inv)
	return s.saveLocked()
}

func (s *Store) FindInvitation(token string) (Invitation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, inv := range s.d.Invitations {
		if inv.Token == token {
			return inv, true
		}
	}
	return Invitation{}, false
}

func (s *Store) MarkInvitationUsed(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, inv := range s.d.Invitations {
		if inv.Token == token {
			s.d.Invitations[i].Used = true
			return s.saveLocked()
		}
	}
	return errors.New("invitation not found")
}

func (s *Store) ListInvitationsForDeployment(deploymentID string) []Invitation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Invitation{}
	for _, inv := range s.d.Invitations {
		if inv.DeploymentID == deploymentID && !inv.Used && time.Now().Before(inv.ExpiresAt) {
			out = append(out, inv)
		}
	}
	return out
}

// ---- Backups ----

func (s *Store) ListBackupsForDeployment(deploymentID string) []Backup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Backup{}
	for _, b := range s.d.Backups {
		if b.DeploymentID == deploymentID {
			out = append(out, b)
		}
	}
	return out
}

func (s *Store) FindBackupByID(id string) (Backup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.d.Backups {
		if b.ID == id {
			return b, true
		}
	}
	return Backup{}, false
}

func (s *Store) AddBackup(b Backup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Backups = append(s.d.Backups, b)
	return s.saveLocked()
}

func (s *Store) DeleteBackup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, b := range s.d.Backups {
		if b.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("backup not found")
	}
	s.d.Backups = append(s.d.Backups[:idx], s.d.Backups[idx+1:]...)
	return s.saveLocked()
}

// ---- Schedules ----

func (s *Store) ListSchedulesForDeployment(deploymentID string) []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Schedule{}
	for _, sc := range s.d.Schedules {
		if sc.DeploymentID == deploymentID {
			out = append(out, sc)
		}
	}
	return out
}

func (s *Store) ListAllSchedules() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Schedule, len(s.d.Schedules))
	copy(out, s.d.Schedules)
	return out
}

func (s *Store) FindScheduleByID(id string) (Schedule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sc := range s.d.Schedules {
		if sc.ID == id {
			return sc, true
		}
	}
	return Schedule{}, false
}

func (s *Store) AddSchedule(sc Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Schedules = append(s.d.Schedules, sc)
	return s.saveLocked()
}

func (s *Store) UpdateSchedule(sc Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.d.Schedules {
		if existing.ID == sc.ID {
			s.d.Schedules[i] = sc
			return s.saveLocked()
		}
	}
	return errors.New("schedule not found")
}

func (s *Store) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, sc := range s.d.Schedules {
		if sc.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("schedule not found")
	}
	s.d.Schedules = append(s.d.Schedules[:idx], s.d.Schedules[idx+1:]...)
	return s.saveLocked()
}

// ---- Activities ----

func (s *Store) ListActivities(limit int) []ActivityEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := []ActivityEntry{}
	n := len(s.d.Activities)
	start := n - limit
	if start < 0 {
		start = 0
	}
	for i := n - 1; i >= start; i-- {
		out = append(out, s.d.Activities[i])
	}
	return out
}

func (s *Store) AddActivity(a ActivityEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Activities = append(s.d.Activities, a)
	if len(s.d.Activities) > 2000 {
		s.d.Activities = s.d.Activities[len(s.d.Activities)-2000:]
	}
	return s.saveLocked()
}

// ---- API Tokens ----

func (s *Store) ListAPITokensForUser(userID string) []APIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []APIToken{}
	for _, t := range s.d.APITokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) FindAPITokenByHash(hash string) (APIToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.d.APITokens {
		if t.TokenHash == hash && !t.Revoked {
			return t, true
		}
	}
	return APIToken{}, false
}

func (s *Store) FindAPITokenByID(id string) (APIToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.d.APITokens {
		if t.ID == id {
			return t, true
		}
	}
	return APIToken{}, false
}

func (s *Store) AddAPIToken(t APIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.APITokens = append(s.d.APITokens, t)
	return s.saveLocked()
}

func (s *Store) RevokeAPIToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.d.APITokens {
		if t.ID == id {
			s.d.APITokens[i].Revoked = true
			return s.saveLocked()
		}
	}
	return errors.New("token not found")
}

func (s *Store) TouchAPIToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.d.APITokens {
		if t.ID == id {
			s.d.APITokens[i].LastUsed = time.Now()
			return s.saveLocked()
		}
	}
	return errors.New("token not found")
}

// ---- Notifications ----

func (s *Store) ListNotifications(userID string, limit int) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []Notification{}
	for i := len(s.d.Notifications) - 1; i >= 0 && len(out) < limit; i-- {
		n := s.d.Notifications[i]
		if n.UserID == "" || n.UserID == userID {
			out = append(out, n)
		}
	}
	return out
}

func (s *Store) AddNotification(n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Notifications = append(s.d.Notifications, n)
	if len(s.d.Notifications) > 500 {
		s.d.Notifications = s.d.Notifications[len(s.d.Notifications)-500:]
	}
	return s.saveLocked()
}

func (s *Store) MarkNotificationRead(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.d.Notifications {
		if n.ID == id {
			s.d.Notifications[i].Read = true
			return s.saveLocked()
		}
	}
	return errors.New("notification not found")
}

// ---- Domains ----

func (s *Store) ListDomainsForDeployment(deploymentID string) []Domain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Domain{}
	for _, d := range s.d.Domains {
		if d.DeploymentID == deploymentID {
			out = append(out, d)
		}
	}
	return out
}

func (s *Store) ListAllDomains() []Domain {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Domain, len(s.d.Domains))
	copy(out, s.d.Domains)
	return out
}

func (s *Store) FindDomainByID(id string) (Domain, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Domains {
		if d.ID == id {
			return d, true
		}
	}
	return Domain{}, false
}

func (s *Store) FindDomainByHostname(hostname string) (Domain, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Domains {
		if strings.EqualFold(d.Hostname, hostname) {
			return d, true
		}
	}
	return Domain{}, false
}

func (s *Store) AddDomain(d Domain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Domains {
		if strings.EqualFold(existing.Hostname, d.Hostname) {
			return errors.New("this domain is already configured")
		}
	}
	s.d.Domains = append(s.d.Domains, d)
	return s.saveLocked()
}

func (s *Store) UpdateDomain(d Domain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.d.Domains {
		if existing.ID == d.ID {
			s.d.Domains[i] = d
			return s.saveLocked()
		}
	}
	return errors.New("domain not found")
}

func (s *Store) DeleteDomain(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.d.Domains {
		if d.ID == id {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("domain not found")
	}
	s.d.Domains = append(s.d.Domains[:idx], s.d.Domains[idx+1:]...)
	return s.saveLocked()
}

// ---- Webhook secret ----

func (s *Store) GetWebhookSecret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.WebhookSecret
}

func (s *Store) SetWebhookSecret(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.WebhookSecret = secret
	return s.saveLocked()
}

func (s *Store) EnsureWebhookSecret(generate func() (string, error)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.WebhookSecret != "" {
		return s.d.WebhookSecret, nil
	}
	secret, err := generate()
	if err != nil {
		return "", err
	}
	s.d.WebhookSecret = secret
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return secret, nil
}