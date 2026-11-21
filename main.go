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
}

type phase int

const (
	phaseLoading phase = iota
	phaseServers
	phaseList
)

type model struct {
	phase      phase
	servers    []server
	serverIdx  int
	recs       []recording
	cursor     int
	width      int
	height     int
	err        string
	status     string
	serverURL  string
	serverName string
}

type serversMsg struct {
	servers []server
	err     error
}

type recordingsMsg struct {
	recs []recording
	err  error
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
		recs, err := parseRecordingsJSON(out)
		if err != nil {
			return recordingsMsg{err: err}
		}
		if len(recs) == 0 {
			return recordingsMsg{err: fmt.Errorf("録画が見つかりませんでした")}
		}
		return recordingsMsg{recs: recs}
	}
}

func findPython() string {
	// Try uv tool's python (common install location)
	uvPython := filepath.Join(os.Getenv("HOME"), ".local", "share", "uv", "tools", "pydtcpip", "bin", "python")
	if _, err := os.Stat(uvPython); err == nil {
		return uvPython
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

type browseRecording struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Date      string `json:"date"`
	Duration  string `json:"duration"`
	Size      int64  `json:"size"`
	Encrypted bool   `json:"encrypted"`
	URL       string `json:"url"`
}

func parseRecordingsJSON(data []byte) ([]recording, error) {
	var items []browseRecording
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	var recs []recording
	for _, item := range items {
		rec := recording{
			ID:        item.ID,
			Title:     strings.TrimSpace(item.Title),
			Date:      strings.TrimSpace(item.Date),
			Duration:  item.Duration,
			Size:      item.Size,
			Encrypted: item.Encrypted,
			URL:       item.URL,
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
	return recs, nil
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
		m.recs = msg.recs
		m.cursor = 0
		m.phase = phaseList
		m.err = ""
		if len(msg.recs) == 0 {
			m.status = "録画が見つかりませんでした。"
		} else {
			m.status = ""
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
			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.recs)-1 {
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
				if len(m.recs) > 0 {
					m.status = "再生を開始しています..."
					return m, playCmd(m.serverURL, m.recs[m.cursor])
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
	b.WriteString(titleStyle.Render(header) + "\n\n")

	height := m.height
	if height <= 0 {
		height = 24
	}
	rows := height - 4
	if rows < 1 {
		rows = 1
	}
	start := 0
	if m.cursor >= rows {
		start = m.cursor - rows + 1
	}
	end := start + rows
	if end > len(m.recs) {
		end = len(m.recs)
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	for i := start; i < end; i++ {
		rec := m.recs[i]
		prefix := "E"
		if !rec.Encrypted {
			prefix = "F"
		}
		line := fmt.Sprintf("%s  %s  %7s  %6s  %s",
			prefix, shortDate(rec.Date), shortDuration(rec.Duration), humanSize(rec.Size), rec.Title)
		if i == m.cursor {
			b.WriteString(selStyle.MaxWidth(width).Render(line) + "\n")
		} else {
			b.WriteString(dimStyle.MaxWidth(width).Render(line) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("↑↓: 選択  P: 再生  r: 更新  q: 終了"))
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
		recs, err := parseRecordingsJSON(out)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			prefix := "E"
			if !rec.Encrypted {
				prefix = "F"
			}
			fmt.Printf("%s  %s  %7s  %6s  %s\n",
				prefix, shortDate(rec.Date), shortDuration(rec.Duration), humanSize(rec.Size), rec.Title)
		}
	}
	return nil
}

func main() {
	list := flag.Bool("list", false, "list recordings without the terminal UI")
	serverURL := flag.String("server", "", "device description URL (skip discovery)")
	flag.Parse()

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
