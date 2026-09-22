package handlers

import (
	"regexp"
	"strconv"
	"strings"
)

// ParsedError représente une erreur extraite d'un log de console.
type ParsedError struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message,omitempty"`
	Raw     string `json:"raw"`
}

// ---- Patterns d'erreurs par langage ----

var (
	// Node.js : "at Object.<anonymous> (/app/server.js:12:34)"
	//           "at /app/index.js:8:1"
	nodeStackRe = regexp.MustCompile(`(?:\(|\s)(/app/[^:\s)]+\.(?:js|ts|mjs|cjs|jsx|tsx)):(\d+):(\d+)`)

	// Node.js message d'erreur générique : "Error: Cannot find module 'xyz'"
	nodeErrMsgRe = regexp.MustCompile(`^(\w*Error):\s*(.+)$`)

	// Python traceback : `File "/app/app.py", line 42, in <module>`
	pythonStackRe = regexp.MustCompile(`File "(/app/[^"]+\.py)", line (\d+)`)

	// Python exception finale : "ValueError: something went wrong"
	pythonErrMsgRe = regexp.MustCompile(`^(\w*Error|Exception|Warning):\s*(.+)$`)

	// PHP Fatal : "PHP Fatal error:  Uncaught Error: ... in /var/www/html/app.php on line 42"
	phpErrorRe = regexp.MustCompile(`in (/var/www/html/[^\s]+\.php) on line (\d+)`)

	// Go panic : "panic: runtime error: ... \n\t/main.go:42 +0x1a"
	goPanicRe = regexp.MustCompile(`(/app/[^\s:]+\.go):(\d+)`)
)

// ParseErrorLog analyse un bloc de texte (logs de la console) et en extrait
// toutes les erreurs avec fichier + ligne si possible.
func ParseErrorLog(logText string) []ParsedError {
	out := []ParsedError{}
	lines := strings.Split(logText, "\n")

	var lastMsg string

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		// Node.js
		if m := nodeStackRe.FindStringSubmatch(line); len(m) == 4 {
			lineNum, _ := strconv.Atoi(m[2])
			colNum, _ := strconv.Atoi(m[3])
			rel := strings.TrimPrefix(m[1], "/app/")
			out = append(out, ParsedError{
				File:    rel,
				Line:    lineNum,
				Column:  colNum,
				Message: lastMsg,
				Raw:     raw,
			})
			continue
		}

		// Python
		if m := pythonStackRe.FindStringSubmatch(line); len(m) == 3 {
			lineNum, _ := strconv.Atoi(m[2])
			rel := strings.TrimPrefix(m[1], "/app/")
			out = append(out, ParsedError{
				File:    rel,
				Line:    lineNum,
				Message: lastMsg,
				Raw:     raw,
			})
			continue
		}

		// PHP
		if m := phpErrorRe.FindStringSubmatch(line); len(m) == 3 {
			lineNum, _ := strconv.Atoi(m[2])
			rel := strings.TrimPrefix(m[1], "/var/www/html/")
			out = append(out, ParsedError{
				File:    rel,
				Line:    lineNum,
				Message: lastMsg,
				Raw:     raw,
			})
			continue
		}

		// Go
		if m := goPanicRe.FindStringSubmatch(line); len(m) == 3 {
			lineNum, _ := strconv.Atoi(m[2])
			rel := strings.TrimPrefix(m[1], "/app/")
			out = append(out, ParsedError{
				File:    rel,
				Line:    lineNum,
				Message: lastMsg,
				Raw:     raw,
			})
			continue
		}

		// Mémoriser le dernier message d'erreur vu (pour l'associer à la stack)
		if m := nodeErrMsgRe.FindStringSubmatch(line); len(m) == 3 {
			lastMsg = line
		}
		if m := pythonErrMsgRe.FindStringSubmatch(line); len(m) == 3 {
			lastMsg = line
		}
	}

	return out
}

// LastError : renvoie la dernière erreur détectée (la plus profonde dans la stack)
// ou nil si aucune erreur avec fichier + ligne n'a été trouvée.
func LastError(logText string) *ParsedError {
	errs := ParseErrorLog(logText)
	if len(errs) == 0 {
		return nil
	}
	return &errs[len(errs)-1]
}