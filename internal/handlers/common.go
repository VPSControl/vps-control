package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
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

// runStreamCommand démarre une commande et renvoie son stdout comme un flux.
// Utilisé pour `docker logs -f` (streaming SSE vers le frontend).
func runStreamCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}