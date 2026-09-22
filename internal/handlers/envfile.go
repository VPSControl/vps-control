package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vpscontrol/internal/store"
)

// WriteEnvFile écrit les variables dans {appPath}/.env de manière atomique.
func WriteEnvFile(appPath string, vars []store.EnvVar) (string, error) {
	if appPath == "" {
		return "", fmt.Errorf("empty app path")
	}
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		return "", err
	}
	envPath := filepath.Join(appPath, ".env")

	// Trier pour un fichier stable
	sorted := make([]store.EnvVar, len(vars))
	copy(sorted, vars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	var sb strings.Builder
	for _, v := range sorted {
		val := v.Value
		if strings.ContainsAny(val, " \t\n\"'") {
			val = `"` + strings.ReplaceAll(val, `"`, `\"`) + `"`
		}
		sb.WriteString(v.Key)
		sb.WriteString("=")
		sb.WriteString(val)
		sb.WriteString("\n")
	}

	tmp := envPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, envPath); err != nil {
		return "", err
	}
	return "", nil
}