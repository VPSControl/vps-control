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

// runCommand runs a command with a sane timeout and returns combined
// stdout+stderr (handy for surfacing docker/git errors to the user).
func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
