package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// User représente un compte admin du panel (indépendant des comptes SSH/root du VPS).
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	Salt         string    `json:"salt"`
	Role         string    `json:"role"` // "admin" ou "viewer"
	CreatedAt    time.Time `json:"createdAt"`
}

// DBConnection est une connexion enregistrée vers une base de données externe.
type DBConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"` // "mysql" ou "postgres"
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

// Deployment est une application déployée sur le VPS.
type Deployment struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Stack       string    `json:"stack"`
	SourceType  string    `json:"sourceType"` // "git" ou "upload"
	SourceRef   string    `json:"sourceRef"`  // url git ou nom du zip
	Path        string    `json:"path"`
	Port        string    `json:"port"`
	Container   string    `json:"container"`
	CreatedAt   time.Time `json:"createdAt"`
}

type data struct {
	Users       []User         `json:"users"`
	DBConns     []DBConnection `json:"dbConnections"`
	Deployments []Deployment   `json:"deployments"`
}

// Store est un stockage JSON simple protégé par mutex. Suffisant pour un
// panel d'admin avec peu de comptes et peu de déploiements ; pas fait pour
// des milliers d'écritures concurrentes.
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

// saveLocked écrit le fichier. Le mutex doit déjà être verrouillé (write lock) par l'appelant.
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
			return errors.New("ce nom d'utilisateur existe déjà")
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
		return errors.New("utilisateur introuvable")
	}
	if s.d.Users[idx].Role == "admin" && adminCount <= 1 {
		return errors.New("impossible de supprimer le dernier compte admin")
	}
	s.d.Users = append(s.d.Users[:idx], s.d.Users[idx+1:]...)
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
		return errors.New("connexion introuvable")
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

func (s *Store) AddDeployment(dep Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Deployments {
		if existing.Name == dep.Name {
			return errors.New("un déploiement avec ce nom existe déjà")
		}
	}
	s.d.Deployments = append(s.d.Deployments, dep)
	return s.saveLocked()
}

func (s *Store) DeleteDeployment(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.d.Deployments {
		if d.Name == name {
			idx = i
		}
	}
	if idx == -1 {
		return errors.New("déploiement introuvable")
	}
	s.d.Deployments = append(s.d.Deployments[:idx], s.d.Deployments[idx+1:]...)
	return s.saveLocked()
}
