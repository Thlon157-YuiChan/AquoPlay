package recorder

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed stream.py
var streamScript []byte

// StreamProcess is a running stream.py invocation whose Stdout yields the
// decoded MPEG-2 TS bytes.
type StreamProcess struct {
	Cmd    *exec.Cmd
	Stdout io.ReadCloser
	cancel context.CancelFunc
	clean  func()
}

// StartStream launches stream.py for the given recording and returns a handle
// whose Stdout is the decoded byte stream. The caller must call Close when
// done.
func StartStream(ctx context.Context, serverURL, recordingID string) (*StreamProcess, error) {
	tmpDir, err := os.MkdirTemp("", "dtcpip-stream-")
	if err != nil {
		return nil, err
	}
	clean := func() { os.RemoveAll(tmpDir) }

	scriptPath := filepath.Join(tmpDir, "stream.py")
	if err := os.WriteFile(scriptPath, streamScript, 0o644); err != nil {
		clean()
		return nil, err
	}

	pythonBin := FindPython()
	if pythonBin == "" {
		clean()
		return nil, ErrNoPython
	}

	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, pythonBin, scriptPath, serverURL, recordingID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		clean()
		return nil, err
	}
	// Capture stderr to a file? Keep simple: inherit? We'll set a pipe but drain in goroutine.
	stderr, _ := cmd.StderrPipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := stderr.Read(buf)
			if n > 0 {
				// discard; callers can override by setting cmd.Stderr
			}
			if rerr != nil {
				return
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		cancel()
		clean()
		return nil, err
	}

	return &StreamProcess{Cmd: cmd, Stdout: stdout, cancel: cancel, clean: clean}, nil
}

// Close terminates the stream and removes temp files.
func (s *StreamProcess) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.Stdout != nil {
		s.Stdout.Close()
	}
	err := s.Cmd.Wait()
	if s.clean != nil {
		s.clean()
	}
	return err
}

// PlayCommand builds an exec.Cmd that runs stream.py piped into a player.
// It returns the command plus a cleanup function to remove temp files; the
// caller should invoke cleanup after the command finishes (e.g. in the
// tea.ExecProcess callback).
func PlayCommand(serverURL string, rec Recording, playerBin, playerArgs string) (*exec.Cmd, func(), error) {
	tmpDir, err := os.MkdirTemp("", "dtcpip-stream-")
	if err != nil {
		return nil, func() {}, err
	}
	clean := func() { os.RemoveAll(tmpDir) }

	scriptPath := filepath.Join(tmpDir, "stream.py")
	if err := os.WriteFile(scriptPath, streamScript, 0o644); err != nil {
		clean()
		return nil, func() {}, err
	}

	pythonBin := FindPython()
	if pythonBin == "" {
		clean()
		return nil, func() {}, ErrNoPython
	}

	title := ShellQuote(rec.Title)
	_ = title
	script := fmt.Sprintf("%s %s %s %s | %s %s",
		pythonBin, scriptPath, ShellQuote(serverURL), ShellQuote(rec.ID), playerBin, playerArgs)
	cmd := exec.Command("sh", "-c", script)
	return cmd, clean, nil
}

// ShellQuote single-quotes a string for safe use in a shell command.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
