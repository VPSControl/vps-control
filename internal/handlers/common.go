package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"time"
)

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runCommand exécute une commande avec un timeout raisonnable et retourne
// stdout+stderr combinés (utile pour afficher les erreurs docker/git à l'utilisateur).
func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
