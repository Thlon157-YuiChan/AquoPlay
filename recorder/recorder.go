// Package recorder provides a reusable Go frontend for pydtcpip: discovery of
// DTCP-IP recorders, browsing of their folder/recording trees, and streaming
// of a selected recording as a decoded MPEG-2 TS byte stream.
//
// The package embeds the Python helper scripts (browse.py, stream.py) that
// drive pydtcpip, so callers only need a working Python interpreter with
// pydtcpip installed.
package recorder

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed browse.py
var browseScript []byte

// Server describes a discovered DTCP-IP recorder.
type Server struct {
	Name  string
	Model string
	URL   string
}

// Folder describes a container in the recorder's content tree.
type Folder struct {
	ID         string
	Title      string
	ParentID   string
	ChildCount *int
}

// Recording describes a single recording item.
type Recording struct {
	ID        string
	ParentID  string
	Title     string
	Date      string
	Duration  string
	Size      int64
	Encrypted bool
	URL       string
	Broadcast bool
}

// BrowseResult is the full content tree of a recorder.
type BrowseResult struct {
	Folders    []Folder
	Recordings []Recording
}

// ErrNoPython is returned when no Python interpreter with pydtcpip is found.
var ErrNoPython = errors.New("pydtcpip が使える Python が見つかりません")

// FindPython locates a Python interpreter that can import pydtcpip.
func FindPython() string {
	uvPython := filepath.Join(os.Getenv("HOME"), ".local", "share", "uv", "tools", "pydtcpip", "bin", "python")
	if _, err := os.Stat(uvPython); err == nil {
		return uvPython
	}
	for _, name := range []string{"python3.13", "python3"} {
		if path, err := exec.LookPath(name); err == nil {
			check := exec.Command(path, "-c", "import pydtcpip")
			if check.Run() == nil {
				return path
			}
		}
	}
	if uvPath, err := exec.LookPath("uv"); err == nil {
		_ = uvPath
	}
	return ""
}

// Discover runs `pydtcpip discover` and parses its table output.
func Discover(ctx context.Context, timeoutSec int) ([]Server, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	cmd := exec.CommandContext(ctx, "pydtcpip", "discover", "--timeout", fmt.Sprintf("%d", timeoutSec))
	cmd.Env = append(os.Environ(), "COLUMNS=300")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
	}
	servers := ParseServers(string(out))
	if len(servers) == 0 {
		return nil, fmt.Errorf("レコーダーが見つかりませんでした")
	}
	return servers, nil
}

// ParseServers parses the table printed by `pydtcpip discover`.
func ParseServers(out string) []Server {
	var servers []Server
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "│") {
			continue
		}
		parts := strings.Split(line, "│")
		if len(parts) < 6 {
			continue
		}
		url := strings.TrimSpace(parts[5])
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			continue
		}
		servers = append(servers, Server{
			Name:  strings.TrimSpace(parts[1]),
			Model: strings.TrimSpace(parts[3]),
			URL:   url,
		})
	}
	return servers
}

// Browse enumerates folders and recordings on a recorder.
func Browse(ctx context.Context, serverURL string) (BrowseResult, error) {
	tmpDir, err := os.MkdirTemp("", "dtcpip-browse-")
	if err != nil {
		return BrowseResult{}, err
	}
	defer os.RemoveAll(tmpDir)
	scriptPath := filepath.Join(tmpDir, "browse.py")
	if err := os.WriteFile(scriptPath, browseScript, 0o644); err != nil {
		return BrowseResult{}, err
	}

	pythonBin := FindPython()
	if pythonBin == "" {
		return BrowseResult{}, ErrNoPython
	}

	cmd := exec.CommandContext(ctx, pythonBin, scriptPath, serverURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return BrowseResult{}, fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
	}
	res, err := ParseBrowseResult(out)
	if err != nil {
		return BrowseResult{}, err
	}
	if len(res.Recordings) == 0 && len(res.Folders) == 0 {
		return BrowseResult{}, fmt.Errorf("録画が見つかりませんでした")
	}
	return res, nil
}

// browseFolder is the JSON shape emitted by browse.py.
type browseFolder struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ParentID   string `json:"parent_id"`
	ChildCount *int   `json:"child_count"`
}

// browseRecording is the JSON shape emitted by browse.py.
type browseRecording struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ParentID  string `json:"parent_id"`
	Date      string `json:"date"`
	Duration  string `json:"duration"`
	Size      int64  `json:"size"`
	Encrypted bool   `json:"encrypted"`
	URL       string `json:"url"`
	Broadcast bool   `json:"broadcast"`
}

// browseResultJSON is the top-level JSON emitted by browse.py.
type browseResultJSON struct {
	Folders    []browseFolder    `json:"folders"`
	Recordings []browseRecording `json:"recordings"`
}

// ParseBrowseResult parses the JSON output of browse.py.
func ParseBrowseResult(data []byte) (BrowseResult, error) {
	var result browseResultJSON
	if err := json.Unmarshal(data, &result); err != nil {
		return BrowseResult{}, err
	}
	var folders []Folder
	for _, f := range result.Folders {
		folders = append(folders, Folder{
			ID:         f.ID,
			Title:      f.Title,
			ParentID:   f.ParentID,
			ChildCount: f.ChildCount,
		})
	}
	var recs []Recording
	for _, item := range result.Recordings {
		rec := Recording{
			ID:        item.ID,
			ParentID:  item.ParentID,
			Title:     strings.TrimSpace(item.Title),
			Date:      strings.TrimSpace(item.Date),
			Duration:  item.Duration,
			Size:      item.Size,
			Encrypted: item.Encrypted,
			URL:       item.URL,
			Broadcast: item.Broadcast,
		}
		if rec.Title == "" {
			rec.Title = filepath.Base(item.ID)
		}
		recs = append(recs, rec)
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].Date == recs[j].Date {
			return recs[i].Title < recs[j].Title
		}
		return recs[i].Date > recs[j].Date
	})
	return BrowseResult{Folders: folders, Recordings: recs}, nil
}
