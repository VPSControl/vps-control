package handlers

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vpscontrol/internal/middleware"
)

// FileHandlers exposes a file manager scoped to Root: it's impossible to
// escape this directory (protects against ../.. path traversal).
type FileHandlers struct {
	Root string
}

// resolve cleans up the requested path and checks it stays under Root.
func (h *FileHandlers) resolve(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	cleaned := filepath.Clean("/" + rel) // forces an absolute path relative to Root, blocks "../.."
	full := filepath.Join(h.Root, cleaned)
	rootAbs, err := filepath.Abs(h.Root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("path outside of the allowed directory")
	}
	return fullAbs, nil
}

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func (h *FileHandlers) List(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		middleware.JSONError(w, http.StatusNotFound, "directory not found: "+err.Error())
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{
			Name:    e.Name(),
			Path:    filepath.Join(rel, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	middleware.JSON(w, http.StatusOK, out)
}

const maxEditableFileSize = 2 * 1024 * 1024 // 2 MB: beyond that, only download is offered

func (h *FileHandlers) ReadContent(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		middleware.JSONError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		middleware.JSONError(w, http.StatusBadRequest, "this is a directory")
		return
	}
	if info.Size() > maxEditableFileSize {
		middleware.JSONError(w, http.StatusRequestEntityTooLarge, "file too large for the editor, use download instead")
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{"content": string(b)})
}

func (h *FileHandlers) WriteContent(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEditableFileSize+1))
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "error reading the content")
		return
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) Mkdir(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (h *FileHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" || rel == "." || rel == "/" {
		middleware.JSONError(w, http.StatusBadRequest, "refusing to delete the root directory")
		return
	}
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.RemoveAll(full); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) Download(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		middleware.JSONError(w, http.StatusNotFound, "file not found")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(full)+"\"")
	http.ServeFile(w, r, full)
}

const maxUploadSize = 200 * 1024 * 1024 // 200 MB

func (h *FileHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	fullDir, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "upload too large or invalid")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no file received (expected field 'file')")
		return
	}
	saved := make([]string, 0, len(files))
	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			continue
		}
		dstPath := filepath.Join(fullDir, filepath.Base(fh.Filename))
		dst, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			continue
		}
		_, _ = io.Copy(dst, src)
		src.Close()
		dst.Close()
		saved = append(saved, fh.Filename)
	}
	middleware.JSON(w, http.StatusCreated, map[string]interface{}{"saved": saved})
}
