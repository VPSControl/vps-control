package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// PortDetection décrit le résultat de la détection automatique.
type PortDetection struct {
	Port     string `json:"port"`
	Source   string `json:"source"`   // ex: ".env", "Dockerfile EXPOSE", "framework default"
	Detected bool   `json:"detected"` // true si trouvé dans un fichier, false si fallback
}

// detectPort essaie de trouver le port interne exposé par le projet.
// Retourne le port tel que trouvé (string), ou "" si rien trouvé.
func detectPort(dir, stack string) PortDetection {
	// 1. .env du projet
	if p := portFromEnvFile(dir); p != "" {
		return PortDetection{Port: p, Source: ".env", Detected: true}
	}

	// 2. Dockerfile EXPOSE
	if p := portFromDockerfile(dir); p != "" {
		return PortDetection{Port: p, Source: "Dockerfile EXPOSE", Detected: true}
	}

	// 3. package.json → scripts.start
	if p := portFromPackageJSON(dir); p != "" {
		return PortDetection{Port: p, Source: "package.json start script", Detected: true}
	}

	// 4. vite.config.js → server.port
	if p := portFromViteConfig(dir); p != "" {
		return PortDetection{Port: p, Source: "vite.config", Detected: true}
	}

	// 5. Framework par défaut
	if p := portFromStackDefault(stack); p != "" {
		return PortDetection{Port: p, Source: "framework default", Detected: false}
	}

	return PortDetection{Port: "80", Source: "fallback", Detected: false}
}

// portFromEnvFile lit un éventuel .env à la racine.
func portFromEnvFile(dir string) string {
	for _, name := range []string{".env", ".env.local", ".env.production"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`(?m)^\s*PORT\s*=\s*["']?(\d{2,5})["']?\s*$`)
		if m := re.FindStringSubmatch(string(b)); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// portFromDockerfile cherche la première ligne EXPOSE.
func portFromDockerfile(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d{2,5})`)
	if m := re.FindStringSubmatch(string(b)); len(m) > 1 {
		return m[1]
	}
	return ""
}

// portFromPackageJSON cherche un port dans scripts.start (ex: "next start -p 4000").
func portFromPackageJSON(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return ""
	}
	start := pkg.Scripts["start"]
	if start == "" {
		return ""
	}
	// Patterns courants : -p 4000, --port 4000, PORT=4000
	re := regexp.MustCompile(`(?:-p|--port)\s+(\d{2,5})`)
	if m := re.FindStringSubmatch(start); len(m) > 1 {
		return m[1]
	}
	re = regexp.MustCompile(`PORT=(\d{2,5})`)
	if m := re.FindStringSubmatch(start); len(m) > 1 {
		return m[1]
	}
	return ""
}

// portFromViteConfig cherche server.port dans vite.config.{js,ts,mjs}.
func portFromViteConfig(dir string) string {
	for _, name := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`port\s*:\s*(\d{2,5})`)
		if m := re.FindStringSubmatch(string(b)); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// portFromStackDefault : port par défaut selon le framework.
func portFromStackDefault(stack string) string {
	switch stack {
	case "laravel":
		return "80"
	case "node":
		return "3000"
	case "react":
		return "80" // nginx dans le conteneur
	case "next":
		return "3000"
	case "astro":
		return "80"
	case "sveltekit":
		return "80"
	case "nuxt":
		return "3000"
	case "python":
		return "5000"
	default:
		return "80"
	}
}

// ---- Gestion des ports hôte (côté VPS) ----

// portIsFree vérifie qu'un port TCP est libre sur la machine hôte.
func portIsFree(port string) bool {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// findFreePort cherche le premier port libre à partir de `start`.
// Utile quand l'utilisateur ne précise pas de port, ou que celui-ci est pris.
func findFreePort(start int, max int) (string, error) {
	if max <= 0 {
		max = start + 100
	}
	for p := start; p < max; p++ {
		s := strconv.Itoa(p)
		if portIsFree(s) {
			return s, nil
		}
	}
	return "", fmt.Errorf("aucun port libre trouvé entre %d et %d", start, max)
}

// suggestHostPort : si le port demandé est libre on le garde,
// sinon on cherche le suivant disponible à partir de `fallbackStart`.
func suggestHostPort(requested string, fallbackStart int) (port string, auto bool, err error) {
	if requested != "" && portIsFree(requested) {
		return requested, false, nil
	}
	p, err := findFreePort(fallbackStart, fallbackStart+100)
	if err != nil {
		return "", false, err
	}
	return p, true, nil
}

// parseEnvPort lit la première valeur PORT trouvée dans un flux texte.
// (Utilisé comme helper si un jour on parse autre chose.)
func parseEnvPort(text string) string {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "PORT=") {
			v := strings.TrimPrefix(line, "PORT=")
			v = strings.Trim(v, `"'`)
			if _, err := strconv.Atoi(v); err == nil {
				return v
			}
		}
	}
	return ""
}