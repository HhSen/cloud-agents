package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type runCommandRequest struct {
	Command string            `json:"command"`
	CWD     string            `json:"cwd"`
	Timeout int               `json:"timeout"`
	Envs    map[string]string `json:"envs,omitempty"`
}

// execdEvent is one line in the NDJSON stream returned by POST /command.
type execdEvent struct {
	Type  string      `json:"type"`
	Text  string      `json:"text"`
	Error *execdError `json:"error,omitempty"`
}

type execdError struct {
	Ename  string `json:"ename"`
	Evalue string `json:"evalue"` // exit code as string, e.g. "128"
}

// cloneRepo runs `git clone <gitURL> .` inside the sandbox's working directory.
// execd POST /command streams NDJSON events; the terminal event has type "error"
// when the process exits non-zero (evalue holds the exit code as a string).
// The command is run with a full PATH so git can locate the ssh binary.
func (m *Manager) cloneRepo(ctx context.Context, sandboxID, gitURL, cwd string) error {
	execdBase := fmt.Sprintf("%s/sandboxes/%s/proxy/44772", m.serverURL, sandboxID)

	// Shell-quote the URL (defense-in-depth; API layer already validated it).
	quotedURL := "'" + strings.ReplaceAll(gitURL, "'", `'\''`) + "'"

	cmdReq := runCommandRequest{
		Command: "git clone " + quotedURL + " .",
		CWD:     cwd,
		Timeout: 300000,
		// execd runs with a minimal PATH; inject a full one so git can find ssh.
		Envs: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
	}
	payload, err := json.Marshal(cmdReq)
	if err != nil {
		return fmt.Errorf("marshal clone command: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, execdBase+"/command", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build clone request: %w", err)
	}
	req.Header.Set("X-OPEN-SANDBOX-API-KEY", m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Dedicated client: timeout must exceed the command's own timeout so execd
	// can stream the full result before the HTTP connection is closed.
	cloneClient := &http.Client{Timeout: 315 * time.Second}
	resp, err := cloneClient.Do(req)
	if err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("git clone: status %d: %s", resp.StatusCode, body)
	}

	// execd streams NDJSON: one JSON object per line, blank lines as separators.
	// Collect stderr/stdout and watch for the terminal "error" event (non-zero exit).
	var stderrBuf, stdoutBuf strings.Builder
	exitCode := 0

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var ev execdEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "stdout":
			stdoutBuf.WriteString(ev.Text)
			stdoutBuf.WriteByte('\n')
		case "stderr":
			stderrBuf.WriteString(ev.Text)
			stderrBuf.WriteByte('\n')
		case "error":
			if ev.Error != nil {
				code, _ := strconv.Atoi(ev.Error.Evalue)
				if code == 0 {
					code = 1 // error event with unparseable evalue is still a failure
				}
				exitCode = code
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read clone response stream: %w", err)
	}

	if exitCode != 0 {
		detail := strings.TrimSpace(stderrBuf.String())
		if detail == "" {
			detail = strings.TrimSpace(stdoutBuf.String())
		}
		return fmt.Errorf("git clone failed (exit %d): %s", exitCode, detail)
	}
	slog.InfoContext(ctx, "git clone completed", "sandboxID", sandboxID)
	return nil
}
