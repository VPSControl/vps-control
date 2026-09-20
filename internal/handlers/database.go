package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type DatabaseHandlers struct {
	Store *store.Store
}

func dsnFor(c store.DBConnection) (driverName, dsn string, err error) {
	switch c.Driver {
	case "mysql":
		return "mysql", fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", c.User, c.Password, c.Host, c.Port, c.DBName), nil
	case "postgres":
		return "postgres", fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", c.Host, c.Port, c.User, c.Password, c.DBName), nil
	default:
		return "", "", fmt.Errorf("unsupported driver: %s", c.Driver)
	}
}

func openDB(c store.DBConnection) (*sql.DB, error) {
	driverName, dsn, err := dsnFor(c)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type connRequest struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

func (h *DatabaseHandlers) AddConnection(w http.ResponseWriter, r *http.Request) {
	var req connRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	c := store.DBConnection{
		ID: randomID(), Name: req.Name, Driver: req.Driver, Host: req.Host,
		Port: req.Port, User: req.User, Password: req.Password, DBName: req.DBName,
	}
	db, err := openDB(c)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	db.Close()
	if err := h.Store.AddDBConnection(c); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, publicConn(c))
}

func publicConn(c store.DBConnection) map[string]interface{} {
	return map[string]interface{}{
		"id": c.ID, "name": c.Name, "driver": c.Driver, "host": c.Host,
		"port": c.Port, "user": c.User, "dbname": c.DBName,
	}
}

func (h *DatabaseHandlers) ListConnections(w http.ResponseWriter, r *http.Request) {
	conns := h.Store.ListDBConnections()
	out := make([]map[string]interface{}, 0, len(conns))
	for _, c := range conns {
		out = append(out, publicConn(c))
	}
	middleware.JSON(w, http.StatusOK, out)
}

func (h *DatabaseHandlers) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := nameFromPath("/api/db/connections/", r.URL.Path)
	if err := h.Store.DeleteDBConnection(id); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// extractConnID pulls the connection id from the start of the path after a
// given prefix, e.g. /api/db/connections/<id>/tables -> <id>
func extractConnID(path, prefix string) (id, rest string) {
	trimmed := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (h *DatabaseHandlers) getConnOr404(w http.ResponseWriter, id string) (store.DBConnection, bool) {
	c, ok := h.Store.FindDBConnection(id)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "connection not found")
		return store.DBConnection{}, false
	}
	return c, true
}

func (h *DatabaseHandlers) ListTables(w http.ResponseWriter, r *http.Request) {
	id, _ := extractConnID(r.URL.Path, "/api/db/connections/")
	id = strings.TrimSuffix(id, "/tables")
	c, ok := h.getConnOr404(w, id)
	if !ok {
		return
	}
	db, err := openDB(c)
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer db.Close()

	var query string
	if c.Driver == "mysql" {
		query = "SHOW TABLES"
	} else {
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'"
	}
	rows, err := db.Query(query)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			tables = append(tables, t)
		}
	}
	middleware.JSON(w, http.StatusOK, tables)
}

var identRe = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

func (h *DatabaseHandlers) TableRows(w http.ResponseWriter, r *http.Request) {
	// path: /api/db/connections/{id}/tables/{table}/rows
	rest := strings.TrimPrefix(r.URL.Path, "/api/db/connections/")
	parts := strings.SplitN(rest, "/", 4) // id, "tables", table, "rows"
	if len(parts) < 3 {
		middleware.JSONError(w, http.StatusBadRequest, "invalid path")
		return
	}
	id, table := parts[0], parts[2]
	if !identRe.MatchString(table) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid table name")
		return
	}
	c, ok := h.getConnOr404(w, id)
	if !ok {
		return
	}
	db, err := openDB(c)
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer db.Close()

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	quoted := table
	if c.Driver == "mysql" {
		quoted = "`" + table + "`"
	} else {
		quoted = `"` + table + `"`
	}
	query := fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", quoted, limit, offset)
	result, err := runSelect(db, query)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, result)
}

type queryRequest struct {
	SQL string `json:"sql"`
}

// RunQuery runs a free-form SQL query. Admin-only (see routing): this is
// deliberately powerful, and therefore dangerous if misused.
func (h *DatabaseHandlers) RunQuery(w http.ResponseWriter, r *http.Request) {
	id, _ := extractConnID(r.URL.Path, "/api/db/connections/")
	id = strings.TrimSuffix(id, "/query")
	c, ok := h.getConnOr404(w, id)
	if !ok {
		return
	}
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	db, err := openDB(c)
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer db.Close()

	trimmed := strings.TrimSpace(strings.ToUpper(req.SQL))
	if strings.HasPrefix(trimmed, "SELECT") || strings.HasPrefix(trimmed, "SHOW") {
		result, err := runSelect(db, req.SQL)
		if err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		middleware.JSON(w, http.StatusOK, result)
		return
	}
	res, err := db.Exec(req.SQL)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	affected, _ := res.RowsAffected()
	middleware.JSON(w, http.StatusOK, map[string]int64{"rowsAffected": affected})
}

// runSelect runs a read query and returns columns + rows in a JSON-friendly shape.
func runSelect(db *sql.DB, query string) (map[string]interface{}, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]interface{}, len(cols))
	scanArgs := make([]interface{}, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	out := []map[string]interface{}{}
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		rowMap := map[string]interface{}{}
		for i, col := range cols {
			v := values[i]
			if b, isBytes := v.([]byte); isBytes {
				rowMap[col] = string(b)
			} else {
				rowMap[col] = v
			}
		}
		out = append(out, rowMap)
	}
	return map[string]interface{}{"columns": cols, "rows": out}, nil
}
