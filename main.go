// Command aquoplay is a small terminal UI for streaming recordings stored on
// a DTCP-IP compatible recorder. It drives the pydtcpip CLI for discovery and
// playback, and hands the decoded MPEG-2 TS stream to ffplay.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Thlon157-YuiChan/AquoPlay/recorder"
)

var (
	pydtcpipBin string
	ffplayBin   string
	mpvBin      string
)

// Type aliases keep the rest of main.go readable while delegating to the
// reusable recorder package.
type folder = recorder.Folder
type server = recorder.Server
type recording = recorder.Recording

type phase int

const (
	phaseLoading phase = iota
	phaseServers
	phaseList
)

type model struct {
	phase         phase
	servers       []server
	serverIdx     int
	folders       []folder
	recs          []recording
	currentFolder string
	viewMode      string // "folders" or "recordings"
	cursor        int
	width         int
	height        int
	err           string
	status        string
	serverURL     string
	serverName    string
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
		servers, err := recorder.Discover(context.Background(), 5)
		if err != nil {
			return serversMsg{err: err}
		}
		if len(servers) == 0 {
			return serversMsg{err: fmt.Errorf("レコーダーが見つかりませんでした")}
		}
		return serversMsg{servers: servers}
	}
}

func browseCmd(url string) tea.Cmd {
	return func() tea.Msg {
		res, err := recorder.Browse(context.Background(), url)
		if err != nil {
			return recordingsMsg{err: err}
		}
		if len(res.Recordings) == 0 && len(res.Folders) == 0 {
			return recordingsMsg{err: fmt.Errorf("録画が見つかりませんでした")}
		}
		return recordingsMsg{folders: res.Folders, recs: res.Recordings}
	}
}

func playCmd(url string, rec recording) tea.Cmd {
	var playerBin string
	var playerArgs string
	title := recorder.ShellQuote(rec.Title)
	if mpvBin != "" {
		playerBin = mpvBin
		playerArgs = fmt.Sprintf("--no-terminal --force-seekable=no --cache=yes --title=%s -", title)
	} else {
		playerBin = ffplayBin
		playerArgs = fmt.Sprintf("-loglevel panic -nostats -autoexit -window_title %s -i pipe:0", title)
	}

	cmd, clean, err := recorder.PlayCommand(url, rec, playerBin, playerArgs)
	if err != nil {
		clean()
		return func() tea.Msg { return playbackDoneMsg{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		clean()
		return playbackDoneMsg{err: err}
	})
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
		hasAll := false
		for _, f := range m.folders {
			if f.ID == "ALL" {
				hasAll = true
				break
			}
		}
		if !hasAll && len(m.recs) > 0 {
			m.folders = append([]folder{{ID: "ALL", Title: "すべての録画", ParentID: "0"}}, m.folders...)
		}
		// Determine view mode: show folders if current folder has sub-folders
		subFolders := m.subFolders(m.currentFolder)
		recsInFolder := m.recordingsInFolder(m.currentFolder)
		if len(subFolders) > 0 || len(recsInFolder) > 0 {
			m.viewMode = "mixed"
			m.status = ""
		} else if len(m.recs) > 0 {
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
			subFolders := m.subFolders(m.currentFolder)
			recs := m.recordingsInFolder(m.currentFolder)
			// In "ALL" mode, show all recordings
			if m.currentFolder == "ALL" {
				recs = m.recs
			}
			// Total items = subFolders + recs
			totalItems := len(subFolders) + len(recs)

			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < totalItems-1 {
					m.cursor++
				}
			case "pgup":
				rows := m.height - 4
				if rows < 1 {
					rows = 1
				}
				m.cursor -= rows
				if m.cursor < 0 {
					m.cursor = 0
				}
			case "pgdown":
				rows := m.height - 4
				if rows < 1 {
					rows = 1
				}
				totalItems := len(m.subFolders(m.currentFolder)) + len(m.recordingsInFolder(m.currentFolder))
				if m.currentFolder == "ALL" {
					totalItems = len(m.subFolders(m.currentFolder)) + len(m.recs)
				}
				m.cursor += rows
				if m.cursor >= totalItems {
					m.cursor = totalItems - 1
				}
				if m.cursor < 0 {
					m.cursor = 0
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
				if m.cursor < len(subFolders) {
					// Open folder
					if len(subFolders) > 0 {
						m.currentFolder = subFolders[m.cursor].ID
						m.cursor = 0
						m.status = ""
					}
				} else {
					// Play recording
					recIdx := m.cursor - len(subFolders)
					if recIdx >= 0 && recIdx < len(recs) {
						m.status = "再生を開始しています..."
						return m, playCmd(m.serverURL, recs[recIdx])
					}
				}
			case "backspace":
				if m.currentFolder == "ALL" {
					m.currentFolder = "0"
					m.cursor = 0
					if len(m.subFolders(m.currentFolder)) > 0 {
						m.viewMode = "folders"
					} else {
						m.viewMode = "recordings"
					}
				} else {
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
						for m.currentFolder != "0" && len(m.subFolders(m.currentFolder)) == 0 && len(m.recordingsInFolder(m.currentFolder)) == 0 && m.currentFolder != "ALL" {
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
				b.WriteString(selStyle.Render("❯ "+label) + "\n")
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
	b.WriteString(dimStyle.Render("フォルダ: "+folderPath) + "\n\n")

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

	subFolders := m.subFolders(m.currentFolder)
	var recs []recording
	if m.currentFolder == "ALL" {
		recs = m.recs
	} else {
		recs = m.recordingsInFolder(m.currentFolder)
	}
	totalItems := len(subFolders) + len(recs)

	startIdx := 0
	if m.cursor >= rows {
		startIdx = m.cursor - rows + 1
	}
	endIdx := startIdx + rows
	if endIdx > totalItems {
		endIdx = totalItems
	}
	for i := startIdx; i < endIdx; i++ {
		if i < len(subFolders) {
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
		} else {
			recIdx := i - len(subFolders)
			rec := recs[recIdx]
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
	}
	b.WriteString("\n" + dimStyle.Render("↑↓/PgUp/PgDn: 選択  P/Enter: 開く/再生  Backspace: 戻る  r: 更新  q: 終了"))
	if m.status != "" {
		b.WriteString("\n" + m.status)
	}
	return b.String()
}

func listRecordings(serverURL string) error {
	servers := []server{{URL: serverURL}}
	if serverURL == "" {
		discovered, err := recorder.Discover(context.Background(), 5)
		if err != nil {
			return err
		}
		servers = discovered
		if len(servers) == 0 {
			return fmt.Errorf("レコーダーが見つかりませんでした")
		}
	}
	for _, s := range servers {
		fmt.Printf("%s  %s  %s\n", s.Name, s.Model, s.URL)

		res, err := recorder.Browse(context.Background(), s.URL)
		if err != nil {
			return err
		}
		for _, rec := range res.Recordings {
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
		discovered, err := recorder.Discover(context.Background(), 5)
		if err != nil {
			return err
		}
		servers = discovered
		if len(servers) == 0 {
			return fmt.Errorf("レコーダーが見つかりませんでした")
		}
	}
	for _, s := range servers {
		fmt.Printf("%s  %s  %s\n", s.Name, s.Model, s.URL)

		res, err := recorder.Browse(context.Background(), s.URL)
		if err != nil {
			return err
		}
		fmt.Println("=== Folders ===")
		for _, f := range res.Folders {
			fmt.Printf("  %s (parent: %s)\n", f.ID, f.ParentID)
		}
		fmt.Println("=== Recordings per folder ===")
		folderCounts := make(map[string]int)
		for _, r := range res.Recordings {
			folderCounts[r.ParentID]++
		}
		for folder, count := range folderCounts {
			fmt.Printf("  %s: %d recordings\n", folder, count)
		}
		fmt.Printf("Total: %d folders, %d recordings\n", len(res.Folders), len(res.Recordings))
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
