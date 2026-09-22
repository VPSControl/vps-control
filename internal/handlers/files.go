package handlers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
)

type FileHandlers struct {
	Root string
}

// resolve : sécurise un chemin relatif et le confine à Root.
func (h *FileHandlers) resolve(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	cleaned := filepath.Clean("/" + rel)
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

// resolveApp : confine un chemin sous le dossier d'une app.
func (h *FileHandlers) resolveApp(depPath, rel string) (string, error) {
	appRoot, err := filepath.Abs(depPath)
	if err != nil {
		return "", err
	}
	if rel == "" {
		rel = "."
	}
	cleaned := filepath.Clean("/" + rel)
	full := filepath.Join(appRoot, cleaned)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != appRoot && !strings.HasPrefix(fullAbs, appRoot+string(os.PathSeparator)) {
		return "", errors.New("path outside of the app directory")
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

// ---- Global (admin) ----

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
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	middleware.JSON(w, http.StatusOK, out)
}

const maxEditableFileSize = 5 * 1024 * 1024

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
		middleware.JSONError(w, http.StatusRequestEntityTooLarge, "file too large for the editor")
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
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) CreateFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(full); err == nil {
		middleware.JSONError(w, http.StatusConflict, "file already exists")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, map[string]bool{"ok": true})
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

type renameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (h *FileHandlers) Rename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	fromFull, err := h.resolve(req.From)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	toFull, err := h.resolve(req.To)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(fromFull); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "source not found")
		return
	}
	if _, err := os.Stat(toFull); err == nil {
		middleware.JSONError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err := os.MkdirAll(filepath.Dir(toFull), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(fromFull, toFull); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) Move(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	destFull, err := h.resolve(req.Dest)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var errs []string
	for _, p := range req.Paths {
		fromFull, err := h.resolve(p)
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		base := filepath.Base(fromFull)
		toFull := filepath.Join(destFull, base)
		if toFull == fromFull {
			continue
		}
		if _, err := os.Stat(fromFull); err != nil {
			errs = append(errs, p+": source not found")
			continue
		}
		if _, err := os.Stat(toFull); err == nil {
			errs = append(errs, p+": destination already exists")
			continue
		}
		if err := os.Rename(fromFull, toFull); err != nil {
			errs = append(errs, p+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		middleware.JSON(w, http.StatusMultiStatus, map[string]interface{}{"ok": false, "errors": errs})
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (h *FileHandlers) DeleteMany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	var errs []string
	for _, p := range req.Paths {
		if p == "" || p == "." || p == "/" {
			continue
		}
		full, err := h.resolve(p)
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			errs = append(errs, p+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		middleware.JSON(w, http.StatusMultiStatus, map[string]interface{}{"ok": false, "errors": errs})
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

func (h *FileHandlers) DownloadZip(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	filename := fmt.Sprintf("vpscontrol-export-%d.zip", time.Now().Unix())
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, p := range req.Paths {
		full, err := h.resolve(p)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		baseName := filepath.Base(full)
		if info.IsDir() {
			_ = filepath.Walk(full, func(path string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				rel, err := filepath.Rel(full, path)
				if err != nil {
					return nil
				}
				entryName := filepath.Join(baseName, rel)
				if fi.IsDir() {
					_, _ = zw.Create(entryName + "/")
					return nil
				}
				fw, err := zw.Create(entryName)
				if err != nil {
					return nil
				}
				src, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer src.Close()
				_, _ = io.Copy(fw, src)
				return nil
			})
		} else {
			fw, err := zw.Create(baseName)
			if err != nil {
				continue
			}
			src, err := os.Open(full)
			if err != nil {
				continue
			}
			_, _ = io.Copy(fw, src)
			src.Close()
		}
	}
}

const maxUploadSize = 500 * 1024 * 1024

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
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "upload too large or invalid")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no file received")
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

func (h *FileHandlers) Extract(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	destRel := r.URL.Query().Get("dest")
	full, err := h.resolve(rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		middleware.JSONError(w, http.StatusNotFound, "archive not found")
		return
	}
	var destFull string
	if destRel != "" {
		destFull, err = h.resolve(destRel)
		if err != nil {
			middleware.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		base := filepath.Base(full)
		dirName := strings.TrimSuffix(base, ".zip")
		dirName = strings.TrimSuffix(dirName, ".tar.gz")
		dirName = strings.TrimSuffix(dirName, ".tgz")
		dirName = strings.TrimSuffix(dirName, ".tar")
		destFull = filepath.Join(filepath.Dir(full), dirName)
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	lower := strings.ToLower(full)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		err = extractZip(full, destFull)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		err = extractTarGz(full, destFull)
	case strings.HasSuffix(lower, ".tar"):
		err = extractTar(full, destFull)
	default:
		middleware.JSONError(w, http.StatusBadRequest, "unsupported archive format")
		return
	}
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "extract failed: "+err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{"ok": true, "dest": destFull})
}

func (h *FileHandlers) Compress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths   []string `json:"paths"`
		Output  string   `json:"output"`
		DestDir string   `json:"destDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	if req.Output == "" {
		req.Output = fmt.Sprintf("archive-%d.zip", time.Now().Unix())
	}
	if !strings.HasSuffix(strings.ToLower(req.Output), ".zip") {
		req.Output += ".zip"
	}
	destDirFull, err := h.resolve(req.DestDir)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(destDirFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	outputPath := filepath.Join(destDirFull, filepath.Base(req.Output))
	outFile, err := os.Create(outputPath)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer outFile.Close()
	zw := zip.NewWriter(outFile)
	defer zw.Close()
	for _, p := range req.Paths {
		full, err := h.resolve(p)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		baseName := filepath.Base(full)
		if info.IsDir() {
			_ = filepath.Walk(full, func(path string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				rel, err := filepath.Rel(full, path)
				if err != nil {
					return nil
				}
				entryName := filepath.Join(baseName, rel)
				if fi.IsDir() {
					_, _ = zw.Create(entryName + "/")
					return nil
				}
				fw, err := zw.Create(entryName)
				if err != nil {
					return nil
				}
				src, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer src.Close()
				_, _ = io.Copy(fw, src)
				return nil
			})
		} else {
			fw, err := zw.Create(baseName)
			if err != nil {
				continue
			}
			src, err := os.Open(full)
			if err != nil {
				continue
			}
			_, _ = io.Copy(fw, src)
			src.Close()
		}
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{"ok": true, "output": filepath.Base(outputPath)})
}

// ---- Extraction helpers ----

func extractZip(src, dest string) error {
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
			return errors.New("invalid zip archive")
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

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	return extractTarReader(gr, dest)
}

func extractTar(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTarReader(f, dest)
}

func extractTarReader(reader io.Reader, dest string) error {
	tr := tar.NewReader(reader)
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fpath := filepath.Join(dest, hdr.Name)
		fpathAbs, err := filepath.Abs(fpath)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(fpathAbs, destAbs+string(os.PathSeparator)) && fpathAbs != destAbs {
			return errors.New("invalid tar archive")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpathAbs, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpathAbs), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(fpathAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return nil
}

// ---- App-scoped handlers ----

func (h *FileHandlers) AppList(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		middleware.JSONError(w, http.StatusNotFound, "directory not found")
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
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	middleware.JSON(w, http.StatusOK, out)
}

func (h *FileHandlers) AppReadContent(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
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
		middleware.JSONError(w, http.StatusRequestEntityTooLarge, "file too large for the editor")
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{"content": string(b)})
}

func (h *FileHandlers) AppWriteContent(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEditableFileSize+1))
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "error reading the content")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) AppUpload(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	fullDir, err := h.resolveApp(dep.Path, rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "upload too large or invalid")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no file received")
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

func (h *FileHandlers) AppDownload(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
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

func (h *FileHandlers) AppCreateFile(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(full); err == nil {
		middleware.JSONError(w, http.StatusConflict, "file already exists")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (h *FileHandlers) AppMkdir(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := h.resolveApp(dep.Path, rel)
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

func (h *FileHandlers) AppRename(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	fromFull, err := h.resolveApp(dep.Path, req.From)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	toFull, err := h.resolveApp(dep.Path, req.To)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(fromFull); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "source not found")
		return
	}
	if _, err := os.Stat(toFull); err == nil {
		middleware.JSONError(w, http.StatusConflict, "destination already exists")
		return
	}
	if err := os.MkdirAll(filepath.Dir(toFull), 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(fromFull, toFull); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) AppMove(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	var req struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	destFull, err := h.resolveApp(dep.Path, req.Dest)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var errs []string
	for _, p := range req.Paths {
		fromFull, err := h.resolveApp(dep.Path, p)
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		base := filepath.Base(fromFull)
		toFull := filepath.Join(destFull, base)
		if toFull == fromFull {
			continue
		}
		if _, err := os.Stat(fromFull); err != nil {
			errs = append(errs, p+": source not found")
			continue
		}
		if _, err := os.Stat(toFull); err == nil {
			errs = append(errs, p+": destination already exists")
			continue
		}
		if err := os.Rename(fromFull, toFull); err != nil {
			errs = append(errs, p+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		middleware.JSON(w, http.StatusMultiStatus, map[string]interface{}{"ok": false, "errors": errs})
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// AppFindFile : cherche un fichier par nom ou chemin relatif.
// GET /api/deployments/{id}/files/find?name=src/index.js
func (h *FileHandlers) AppFindFile(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing name")
		return
	}

	// 1. Essai direct (chemin relatif exact)
	direct, err := h.resolveApp(dep.Path, name)
	if err == nil {
		if _, err := os.Stat(direct); err == nil {
			middleware.JSON(w, http.StatusOK, map[string]interface{}{
				"path":  filepath.ToSlash(name),
				"exact": true,
			})
			return
		}
	}

	// 2. Recherche récursive par nom de base
	appRoot, _ := filepath.Abs(dep.Path)
	baseName := filepath.Base(name)
	var found []string

	_ = filepath.Walk(appRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == ".git" || name == "vendor" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() == baseName {
			rel, err := filepath.Rel(appRoot, path)
			if err == nil {
				found = append(found, filepath.ToSlash(rel))
			}
		}
		return nil
	})

	if len(found) == 0 {
		middleware.JSONError(w, http.StatusNotFound, "file not found: "+name)
		return
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"path":  found[0],
		"exact": false,
		"all":   found,
	})
}

func (h *FileHandlers) AppDelete(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" || rel == "." || rel == "/" {
		middleware.JSONError(w, http.StatusBadRequest, "refusing to delete the app root")
		return
	}
	full, err := h.resolveApp(dep.Path, rel)
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

func (h *FileHandlers) AppDeleteMany(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	var errs []string
	for _, p := range req.Paths {
		if p == "" || p == "." || p == "/" {
			continue
		}
		full, err := h.resolveApp(dep.Path, p)
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			errs = append(errs, p+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		middleware.JSON(w, http.StatusMultiStatus, map[string]interface{}{"ok": false, "errors": errs})
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *FileHandlers) AppDownloadZip(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	filename := fmt.Sprintf("%s-export-%d.zip", dep.Name, time.Now().Unix())
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, p := range req.Paths {
		full, err := h.resolveApp(dep.Path, p)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		baseName := filepath.Base(full)
		if info.IsDir() {
			_ = filepath.Walk(full, func(path string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				rel, err := filepath.Rel(full, path)
				if err != nil {
					return nil
				}
				entryName := filepath.Join(baseName, rel)
				if fi.IsDir() {
					_, _ = zw.Create(entryName + "/")
					return nil
				}
				fw, err := zw.Create(entryName)
				if err != nil {
					return nil
				}
				src, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer src.Close()
				_, _ = io.Copy(fw, src)
				return nil
			})
		} else {
			fw, err := zw.Create(baseName)
			if err != nil {
				continue
			}
			src, err := os.Open(full)
			if err != nil {
				continue
			}
			_, _ = io.Copy(fw, src)
			src.Close()
		}
	}
}

func (h *FileHandlers) AppExtract(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	rel := r.URL.Query().Get("path")
	destRel := r.URL.Query().Get("dest")
	full, err := h.resolveApp(dep.Path, rel)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		middleware.JSONError(w, http.StatusNotFound, "archive not found")
		return
	}
	var destFull string
	if destRel != "" {
		destFull, err = h.resolveApp(dep.Path, destRel)
		if err != nil {
			middleware.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		base := filepath.Base(full)
		dirName := strings.TrimSuffix(base, ".zip")
		dirName = strings.TrimSuffix(dirName, ".tar.gz")
		dirName = strings.TrimSuffix(dirName, ".tgz")
		dirName = strings.TrimSuffix(dirName, ".tar")
		destFull = filepath.Join(filepath.Dir(full), dirName)
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	lower := strings.ToLower(full)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		err = extractZip(full, destFull)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		err = extractTarGz(full, destFull)
	case strings.HasSuffix(lower, ".tar"):
		err = extractTar(full, destFull)
	default:
		middleware.JSONError(w, http.StatusBadRequest, "unsupported archive format")
		return
	}
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "extract failed: "+err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{"ok": true, "dest": destFull})
}

func (h *FileHandlers) AppCompress(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	var req struct {
		Paths   []string `json:"paths"`
		Output  string   `json:"output"`
		DestDir string   `json:"destDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.Paths) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "no paths provided")
		return
	}
	if req.Output == "" {
		req.Output = fmt.Sprintf("archive-%d.zip", time.Now().Unix())
	}
	if !strings.HasSuffix(strings.ToLower(req.Output), ".zip") {
		req.Output += ".zip"
	}
	destDirFull, err := h.resolveApp(dep.Path, req.DestDir)
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(destDirFull, 0o755); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	outputPath := filepath.Join(destDirFull, filepath.Base(req.Output))
	outFile, err := os.Create(outputPath)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer outFile.Close()
	zw := zip.NewWriter(outFile)
	defer zw.Close()
	for _, p := range req.Paths {
		full, err := h.resolveApp(dep.Path, p)
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		baseName := filepath.Base(full)
		if info.IsDir() {
			_ = filepath.Walk(full, func(path string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				rel, err := filepath.Rel(full, path)
				if err != nil {
					return nil
				}
				entryName := filepath.Join(baseName, rel)
				if fi.IsDir() {
					_, _ = zw.Create(entryName + "/")
					return nil
				}
				fw, err := zw.Create(entryName)
				if err != nil {
					return nil
				}
				src, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer src.Close()
				_, _ = io.Copy(fw, src)
				return nil
			})
		} else {
			fw, err := zw.Create(baseName)
			if err != nil {
				continue
			}
			src, err := os.Open(full)
			if err != nil {
				continue
			}
			_, _ = io.Copy(fw, src)
			src.Close()
		}
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{"ok": true, "output": filepath.Base(outputPath)})
}