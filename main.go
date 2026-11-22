// Command aquoplay is a small terminal UI for streaming recordings stored on
// a DTCP-IP compatible recorder. It drives the pydtcpip CLI for discovery and
// playback, and hands the decoded MPEG-2 TS stream to ffplay.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	pydtcpipBin string
	ffplayBin   string
	mpvBin      string
)

//go:embed browse.py
var browseScript []byte

//go:embed stream.py
var streamScript []byte

type folder struct {
	ID       string
	Title    string
	ParentID string
}

type server struct {
	Name  string
	Model string
	URL   string
}

type recording struct {
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

type phase int

const (
	phaseLoading phase = iota
	phaseServers
	phaseList
)

type model struct {
	phase          phase
	servers        []server
	serverIdx      int
	folders        []folder
	recs           []recording
	currentFolder  string
	viewMode       string // "folders" or "recordings"
	cursor         int
	width          int
	height         int
	err            string
	status         string
	serverURL      string
	serverName     string
}

type serversMsg struct {
	servers []server
	err     error
}

type recordingsMsg struct {
	folders []folder
	recs    []recording
	err     error
}

type playbackDoneMsg struct {
	err error
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	selStyle   = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("230"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func discoverCmd() tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("pydtcpip", "discover", "--timeout", "5")
		cmd.Env = append(os.Environ(), "COLUMNS=300")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return serversMsg{err: fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))}
		}
		servers := parseServers(string(out))
		if len(servers) == 0 {
			return serversMsg{err: fmt.Errorf("レコーダーが見つかりませんでした")}
		}
		return serversMsg{servers: servers}
	}
}

func parseServers(out string) []server {
	var servers []server
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
		servers = append(servers, server{
			Name:  strings.TrimSpace(parts[1]),
			Model: strings.TrimSpace(parts[3]),
			URL:   url,
		})
	}
	return servers
}

func browseCmd(url string) tea.Cmd {
	return func() tea.Msg {
		// Write embedded script to temp file
		tmpDir, err := os.MkdirTemp("", "dlna-browse-")
		if err != nil {
			return recordingsMsg{err: err}
		}
		defer os.RemoveAll(tmpDir)
		scriptPath := filepath.Join(tmpDir, "browse.py")
		if err := os.WriteFile(scriptPath, browseScript, 0o644); err != nil {
			return recordingsMsg{err: err}
		}

		// Find a Python interpreter with pydtcpip
		pythonBin := findPython()
		if pythonBin == "" {
			return recordingsMsg{err: fmt.Errorf("pydtcpip が使える Python が見つかりません")}
		}

		cmd := exec.Command(pythonBin, scriptPath, url)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return recordingsMsg{err: fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))}
		}
		folders, recs, err := parseBrowseResult(out)
		if err != nil {
			return recordingsMsg{err: err}
		}
		if len(recs) == 0 && len(folders) == 0 {
			return recordingsMsg{err: fmt.Errorf("録画が見つかりませんでした")}
		}
		return recordingsMsg{folders: folders, recs: recs}
	}
}

func findPython() string {
	// Try uv tool's python (common install location)
	uvPython := filepath.Join(os.Getenv("HOME"), ".local", "share", "uv", "tools", "pydtcpip", "bin", "python")
	if _, err := os.Stat(uvPython); err == nil {
		return uvPython
	}
	// Try python3.13 explicitly
	if path, err := exec.LookPath("python3.13"); err == nil {
		check := exec.Command(path, "-c", "import pydtcpip")
		if check.Run() == nil {
			return path
		}
	}
	// Try system python3 with pydtcpip
	if path, err := exec.LookPath("python3"); err == nil {
		check := exec.Command(path, "-c", "import pydtcpip")
		if check.Run() == nil {
			return path
		}
	}
	// Try uv run (if uv is available)
	if uvPath, err := exec.LookPath("uv"); err == nil {
		// Create a wrapper that uses uv run
		// We return a special marker and handle it in the caller
		_ = uvPath
	}
	return ""
}

type didlLite struct {
	Items []didlItem `xml:"item"`
}

type didlItem struct {
	ID       string    `xml:"id,attr"`
	ParentID string    `xml:"parentID,attr"`
	Title    string    `xml:"title"`
	Date     string    `xml:"date"`
	Res      []didlRes `xml:"res"`
}

type didlRes struct {
	Duration string `xml:"duration,attr"`
	Size     string `xml:"size,attr"`
}

type browseFolder struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ParentID   string `json:"parent_id"`
	ChildCount *int   `json:"child_count"`
}

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

type browseResult struct {
	Folders    []browseFolder    `json:"folders"`
	Recordings []browseRecording `json:"recordings"`
}

func parseBrowseResult(data []byte) ([]folder, []recording, error) {
	var result browseResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, nil, err
	}
	var folders []folder
	for _, f := range result.Folders {
		folders = append(folders, folder{
			ID:       f.ID,
			Title:    f.Title,
			ParentID: f.ParentID,
		})
	}
	var recs []recording
	for _, item := range result.Recordings {
		rec := recording{
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
	return folders, recs, nil
}

func playCmd(url string, rec recording) tea.Cmd {
	// Setup synchronously before returning ExecProcess
	tmpDir, err := os.MkdirTemp("", "dlna-stream-")
	if err != nil {
		return func() tea.Msg { return playbackDoneMsg{err: err} }
	}
	scriptPath := filepath.Join(tmpDir, "stream.py")
	if err := os.WriteFile(scriptPath, streamScript, 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return func() tea.Msg { return playbackDoneMsg{err: err} }
	}

	pythonBin := findPython()
	if pythonBin == "" {
		os.RemoveAll(tmpDir)
		return func() tea.Msg { return playbackDoneMsg{err: fmt.Errorf("pydtcpip が使える Python が見つかりません")} }
	}

	var playerBin string
	var playerArgs string
	title := shellQuote(rec.Title)
	if mpvBin != "" {
		playerBin = mpvBin
		playerArgs = fmt.Sprintf("--no-terminal --force-seekable=no --cache=yes --title=%s -", title)
	} else {
		playerBin = ffplayBin
		playerArgs = fmt.Sprintf("-loglevel panic -nostats -autoexit -window_title %s -i pipe:0", title)
	}

	script := fmt.Sprintf("%s %s %s %s | %s %s",
		pythonBin, scriptPath, shellQuote(url), shellQuote(rec.ID), playerBin, playerArgs)
	c := exec.Command("sh", "-c", script)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		os.RemoveAll(tmpDir)
		return playbackDoneMsg{err: err}
	})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func humanSize(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func shortDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func shortDuration(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		h, _ := strconv.Atoi(parts[0])
		if h == 0 {
			return parts[1] + ":" + parts[2]
		}
		return strconv.Itoa(h) + ":" + parts[1] + ":" + parts[2]
	}
	return s
}

func (m model) Init() tea.Cmd {
	if m.serverURL != "" {
		return browseCmd(m.serverURL)
	}
	return discoverCmd()
}

// subFolders returns folders whose parent is the given folder ID.
func (m model) subFolders(parentID string) []folder {
	var result []folder
	for _, f := range m.folders {
		if f.ParentID == parentID {
			result = append(result, f)
		}
	}
	return result
}

// recordingsInFolder returns recordings whose parent is the given folder ID.
func (m model) recordingsInFolder(folderID string) []recording {
	var result []recording
	for _, r := range m.recs {
		if r.ParentID == folderID {
			result = append(result, r)
		}
	}
	return result
}

// parentFolder returns the parent folder ID, or "" if at root.
func (m model) parentFolder(folderID string) string {
	for _, f := range m.folders {
		if f.ID == folderID {
			return f.ParentID
		}
	}
	return ""
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case serversMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.servers = msg.servers
		if len(msg.servers) == 1 {
			m.serverURL = msg.servers[0].URL
			m.serverName = msg.servers[0].Name
			m.status = "録画を読み込んでいます..."
			m.err = ""
			m.currentFolder = "0"
			return m, browseCmd(m.serverURL)
		}
		m.serverIdx = 0
		m.phase = phaseServers
		return m, nil

	case recordingsMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.phase = phaseList
			return m, nil
		}
		m.folders = msg.folders
		m.recs = msg.recs
		m.cursor = 0
		m.phase = phaseList
		m.err = ""
		if m.currentFolder == "" {
			m.currentFolder = "0"
		}
		// Determine view mode: show folders if current folder has sub-folders
		subFolders := m.subFolders(m.currentFolder)
		recsInFolder := m.recordingsInFolder(m.currentFolder)
		if len(subFolders) > 0 {
			m.viewMode = "folders"
			m.status = ""
		} else if len(recsInFolder) > 0 {
			m.viewMode = "recordings"
			m.status = ""
		} else if len(m.recs) > 0 {
			// Fallback: show all recordings flat if current folder is empty
			m.viewMode = "recordings"
			m.currentFolder = "0"
			m.status = "フォルダ構造が異なるため、全録画を表示しています。"
		} else {
			m.viewMode = "recordings"
			m.status = "録画が見つかりませんでした。"
		}
		return m, nil

	case playbackDoneMsg:
		if msg.err != nil {
			m.status = "再生に失敗しました: " + msg.err.Error()
		} else {
			m.status = ""
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
		switch m.phase {
		case phaseServers:
			switch msg.String() {
			case "up", "k":
				if m.serverIdx > 0 {
					m.serverIdx--
				}
			case "down", "j":
				if m.serverIdx < len(m.servers)-1 {
					m.serverIdx++
				}
			case "p", "P":
				if len(m.servers) > 0 {
					m.serverURL = m.servers[m.serverIdx].URL
					m.serverName = m.servers[m.serverIdx].Name
					m.status = "録画を読み込んでいます..."
					m.err = ""
					return m, browseCmd(m.serverURL)
				}
			}
		case phaseList:
			if m.viewMode == "folders" {
				subFolders := m.subFolders(m.currentFolder)
				switch msg.String() {
				case "up", "k":
					if m.cursor > 0 {
						m.cursor--
					}
				case "down", "j":
					if m.cursor < len(subFolders)-1 {
						m.cursor++
					}
				case "r":
					m.status = "録画を読み込んでいます..."
					m.err = ""
					return m, browseCmd(m.serverURL)
				case "esc":
					if len(m.servers) > 1 {
						m.phase = phaseServers
					}
				case "p", "P", "enter":
					if len(subFolders) > 0 && m.cursor < len(subFolders) {
						m.currentFolder = subFolders[m.cursor].ID
						m.cursor = 0
						// Check if new folder has sub-folders
						if len(m.subFolders(m.currentFolder)) > 0 {
							m.viewMode = "folders"
						} else {
							m.viewMode = "recordings"
						}
						m.status = ""
					}
				case "backspace":
					parent := m.parentFolder(m.currentFolder)
					if parent == "" {
						parent = "0"
					}
					if parent != m.currentFolder {
						m.currentFolder = parent
						m.cursor = 0
						if len(m.subFolders(m.currentFolder)) > 0 {
							m.viewMode = "folders"
						} else {
							m.viewMode = "recordings"
						}
						// If the folder is empty, go up further
						for m.currentFolder != "0" && len(m.subFolders(m.currentFolder)) == 0 && len(m.recordingsInFolder(m.currentFolder)) == 0 {
							p := m.parentFolder(m.currentFolder)
							if p == "" || p == m.currentFolder {
								break
							}
							m.currentFolder = p
							m.cursor = 0
							if len(m.subFolders(m.currentFolder)) > 0 {
								m.viewMode = "folders"
							} else {
								m.viewMode = "recordings"
							}
						}
					}
				}
			} else {
				recs := m.recordingsInFolder(m.currentFolder)
				switch msg.String() {
				case "up", "k":
					if m.cursor > 0 {
						m.cursor--
					}
				case "down", "j":
					if m.cursor < len(recs)-1 {
						m.cursor++
					}
				case "r":
					m.status = "録画を読み込んでいます..."
					m.err = ""
					return m, browseCmd(m.serverURL)
				case "esc":
					if len(m.servers) > 1 {
						m.phase = phaseServers
					}
				case "p", "P":
					if len(recs) > 0 && m.cursor < len(recs) {
						m.status = "再生を開始しています..."
						return m, playCmd(m.serverURL, recs[m.cursor])
					}
				case "backspace":
					parent := m.parentFolder(m.currentFolder)
					if parent == "" {
						parent = "0"
					}
					if parent != m.currentFolder {
						m.currentFolder = parent
						m.cursor = 0
						if len(m.subFolders(m.currentFolder)) > 0 {
							m.viewMode = "folders"
						} else {
							m.viewMode = "recordings"
						}
						// If the folder is empty, go up further
						for m.currentFolder != "0" && len(m.subFolders(m.currentFolder)) == 0 && len(m.recordingsInFolder(m.currentFolder)) == 0 {
							p := m.parentFolder(m.currentFolder)
							if p == "" || p == m.currentFolder {
								break
							}
							m.currentFolder = p
							m.cursor = 0
							if len(m.subFolders(m.currentFolder)) > 0 {
								m.viewMode = "folders"
							} else {
								m.viewMode = "recordings"
							}
						}
					}
				}
			}
		}
		return m, nil
	}
	return m, nil
}

func (m model) View() string {
	if m.err != "" {
		return dimStyle.Render(m.err) + "\n\nq: 終了"
	}
	switch m.phase {
	case phaseServers:
		var b strings.Builder
		b.WriteString(titleStyle.Render("レコーダーを選択") + "\n\n")
		for i, s := range m.servers {
			label := fmt.Sprintf("%s  %s", s.Name, dimStyle.Render(s.Model))
			if i == m.serverIdx {
				b.WriteString(selStyle.Render("❯ " + label) + "\n")
			} else {
				b.WriteString("  " + label + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render("↑↓: 選択  P: 決定  q: 終了"))
		return b.String()
	case phaseList:
		return m.listView()
	default:
		return m.status + "\n\nq: 終了"
	}
}

func (m model) listView() string {
	var b strings.Builder
	header := m.serverName
	if header != "" {
		header += "  " + dimStyle.Render(m.serverURL)
	}
	b.WriteString(titleStyle.Render(header) + "\n")
	folderPath := m.currentFolder
	if folderPath == "" || folderPath == "0" {
		folderPath = "ルート"
	}
	b.WriteString(dimStyle.Render("フォルダ: " + folderPath) + "\n\n")

	height := m.height
	if height <= 0 {
		height = 24
	}
	rows := height - 4
	if rows < 1 {
		rows = 1
	}
	width := m.width
	if width <= 0 {
		width = 80
	}

	if m.viewMode == "folders" {
		subFolders := m.subFolders(m.currentFolder)
		startIdx := 0
		if m.cursor >= rows {
			startIdx = m.cursor - rows + 1
		}
		endIdx := startIdx + rows
		if endIdx > len(subFolders) {
			endIdx = len(subFolders)
		}
		for i := startIdx; i < endIdx; i++ {
			f := subFolders[i]
			title := f.Title
			if title == "" {
				title = f.ID
			}
			line := fmt.Sprintf("[D] %s", title)
			if i == m.cursor {
				b.WriteString(selStyle.MaxWidth(width).Render(line) + "\n")
			} else {
				b.WriteString(dimStyle.MaxWidth(width).Render(line) + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render("↑↓: 選択  P/Enter: 開く  Backspace: 戻る  r: 更新  q: 終了"))
	} else {
		recs := m.recordingsInFolder(m.currentFolder)
		startIdx := 0
		if m.cursor >= rows {
			startIdx = m.cursor - rows + 1
		}
		endIdx := startIdx + rows
		if endIdx > len(recs) {
			endIdx = len(recs)
		}
		for i := startIdx; i < endIdx; i++ {
			rec := recs[i]
			prefix := "E"
			if !rec.Encrypted {
				prefix = "F"
			}
			if rec.Broadcast {
				prefix = "L"
			}
			line := fmt.Sprintf("%s  %s  %7s  %6s  %s",
				prefix, shortDate(rec.Date), shortDuration(rec.Duration), humanSize(rec.Size), rec.Title)
			if i == m.cursor {
				b.WriteString(selStyle.MaxWidth(width).Render(line) + "\n")
			} else {
				b.WriteString(dimStyle.MaxWidth(width).Render(line) + "\n")
			}
		}
		b.WriteString("\n" + dimStyle.Render("↑↓: 選択  P: 再生  Backspace: 戻る  r: 更新  q: 終了"))
	}
	if m.status != "" {
		b.WriteString("\n" + m.status)
	}
	return b.String()
}


func listRecordings(serverURL string) error {
	servers := []server{{URL: serverURL}}
	if serverURL == "" {
		cmd := exec.Command("pydtcpip", "discover", "--timeout", "5")
		cmd.Env = append(os.Environ(), "COLUMNS=300")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
		}
		servers = parseServers(string(out))
		if len(servers) == 0 {
			return fmt.Errorf("レコーダーが見つかりませんでした")
		}
	}
	for _, s := range servers {
		fmt.Printf("%s  %s  %s\n", s.Name, s.Model, s.URL)

		// Write embedded script to temp file
		tmpDir, err := os.MkdirTemp("", "dlna-browse-")
		if err != nil {
			return err
		}
		scriptPath := filepath.Join(tmpDir, "browse.py")
		if err := os.WriteFile(scriptPath, browseScript, 0o644); err != nil {
			os.RemoveAll(tmpDir)
			return err
		}

		pythonBin := findPython()
		if pythonBin == "" {
			os.RemoveAll(tmpDir)
			return fmt.Errorf("pydtcpip が使える Python が見つかりません")
		}

		cmd := exec.Command(pythonBin, scriptPath, s.URL)
		out, err := cmd.CombinedOutput()
		os.RemoveAll(tmpDir)
		if err != nil {
			return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
		}
		_, recs, err := parseBrowseResult(out)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			prefix := "E"
			if !rec.Encrypted {
				prefix = "F"
			}
			if rec.Broadcast {
				prefix = "L"
			}
			fmt.Printf("%s  %s  %7s  %6s  %s\n",
				prefix, shortDate(rec.Date), shortDuration(rec.Duration), humanSize(rec.Size), rec.Title)
		}
	}
	return nil
}

func dumpFolders(serverURL string) error {
	servers := []server{{URL: serverURL}}
	if serverURL == "" {
		cmd := exec.Command("pydtcpip", "discover", "--timeout", "5")
		cmd.Env = append(os.Environ(), "COLUMNS=300")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
		}
		servers = parseServers(string(out))
		if len(servers) == 0 {
			return fmt.Errorf("レコーダーが見つかりませんでした")
		}
	}
	for _, s := range servers {
		fmt.Printf("%s  %s  %s\n", s.Name, s.Model, s.URL)

		tmpDir, err := os.MkdirTemp("", "dlna-browse-")
		if err != nil {
			return err
		}
		scriptPath := filepath.Join(tmpDir, "browse.py")
		if err := os.WriteFile(scriptPath, browseScript, 0o644); err != nil {
			os.RemoveAll(tmpDir)
			return err
		}
		pythonBin := findPython()
		if pythonBin == "" {
			os.RemoveAll(tmpDir)
			return fmt.Errorf("pydtcpip が使える Python が見つかりません")
		}
		cmd := exec.Command(pythonBin, scriptPath, s.URL)
		out, err := cmd.CombinedOutput()
		os.RemoveAll(tmpDir)
		if err != nil {
			return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
		}
		folders, recs, err := parseBrowseResult(out)
		if err != nil {
			return err
		}
		fmt.Println("=== Folders ===")
		for _, f := range folders {
			fmt.Printf("  %s (parent: %s)\n", f.ID, f.ParentID)
		}
		fmt.Println("=== Recordings per folder ===")
		folderCounts := make(map[string]int)
		for _, r := range recs {
			folderCounts[r.ParentID]++
		}
		for folder, count := range folderCounts {
			fmt.Printf("  %s: %d recordings\n", folder, count)
		}
		fmt.Printf("Total: %d folders, %d recordings\n", len(folders), len(recs))
	}
	return nil
}

func main() {
	list := flag.Bool("list", false, "list recordings without the terminal UI")
	folders := flag.Bool("folders", false, "list folder structure without the terminal UI")
	serverURL := flag.String("server", "", "device description URL (skip discovery)")
	flag.Parse()

	if *folders {
		if err := dumpFolders(*serverURL); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *list {
		if err := listRecordings(*serverURL); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	for _, bin := range []string{"pydtcpip", "ffplay"} {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(os.Stderr, "%s が見つかりません。PATH を確認してください。\n", bin)
			os.Exit(1)
		}
	}

	pydtcpipBin, _ = exec.LookPath("pydtcpip")
	ffplayBin, _ = exec.LookPath("ffplay")
	mpvBin, _ = exec.LookPath("mpv")
	if pydtcpipBin == "" {
		pydtcpipBin = "pydtcpip"
	}
	if ffplayBin == "" {
		ffplayBin = "ffplay"
	}
	if os.Getenv("DISPLAY") == "" {
		os.Setenv("DISPLAY", ":0")
	}

	m := model{phase: phaseLoading, status: "レコーダーを探しています..."}
	if *serverURL != "" {
		m.serverURL = *serverURL
		m.status = "録画を読み込んでいます..."
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	if _, err := os.Stat(filepath.Join(dataHome, "pydtcpip", "credentials.env")); err != nil {
		fmt.Fprintf(os.Stderr, "警告: credentials.env が見つかりません (%s)。ストリーミング再生に失敗する可能性があります。\n", filepath.Join(dataHome, "pydtcpip", "credentials.env"))
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
