package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- palette -----------------------------------------------------------------

const (
	colActive = "#38D0FF"
	colDone   = "#5AF78E"
	colMuted  = "#6C6C89"
	colAccent = "#7C6EFF"
	colPink   = "#FF5CA8"
	colErr    = "#FF5F5F"
	colWarn   = "#F3C969"
)

// Layout tuning.
const (
	minTermWidth  = 44
	minTermHeight = 12
	leftMargin    = 1
	rightMargin   = 1
	footerLines   = 1
	logLines      = 4
)

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colActive))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color(colMuted))
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color(colErr))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color(colWarn))
	styleDone   = lipgloss.NewStyle().Foreground(lipgloss.Color(colDone))
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color(colAccent))
	styleKey    = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#0B0B12")).
			Background(lipgloss.Color(colActive)).
			Bold(true).
			Padding(0, 1)
)

// --- state shared with the download goroutines ------------------------------

type fileProgress struct {
	path     string
	total    int64
	done     int64
	outcome  Outcome
	started  bool
	finished bool
}

type tuiState struct {
	mu       sync.Mutex
	name     string
	files    []fileEntry
	progress map[int]*fileProgress
	logs     []string
}

func newTUIState() *tuiState {
	return &tuiState{
		progress: make(map[int]*fileProgress),
	}
}

func (s *tuiState) log(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, msg)
	if len(s.logs) > 200 {
		s.logs = s.logs[len(s.logs)-200:]
	}
}

func (s *tuiState) setName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

func (s *tuiState) setFiles(files []fileEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = files
	for i, f := range files {
		s.progress[i] = &fileProgress{path: sanitizeRelPath(f.RawPath), total: f.Size}
	}
}

func (s *tuiState) setSize(index int, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.progress[index]; p != nil {
		p.total = size
	}
}

func (s *tuiState) begin(index int, path string, total int64, initial int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.progress[index]
	if p == nil {
		p = &fileProgress{}
		s.progress[index] = p
	}
	p.path = path
	p.started = true
	p.done = initial
	if total >= 0 {
		p.total = total
	}
}

func (s *tuiState) advance(index int, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.progress[index]; p != nil {
		p.done += n
	}
}

func (s *tuiState) end(index int, outcome Outcome, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.progress[index]
	if p == nil {
		p = &fileProgress{}
		s.progress[index] = p
	}
	p.path = path
	p.outcome = outcome
	p.finished = true
	switch outcome {
	case OutcomeSkipped, OutcomeDownloaded, OutcomeResumed:
		if p.total < 0 {
			p.total = p.done
		}
		p.done = p.total
	}
}

// percent returns a clamped 0..1 completion ratio for a file.
func (p *fileProgress) percent() float64 {
	if p == nil {
		return 0
	}
	if p.finished && (p.outcome == OutcomeSkipped || p.outcome == OutcomeDownloaded || p.outcome == OutcomeResumed) {
		return 1
	}
	if p.total > 0 {
		v := float64(p.done) / float64(p.total)
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	return 0
}

// snapshot returns a consistent copy of the files and logs for rendering.
func (s *tuiState) snapshot() (string, []fileProgress, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := make([]fileProgress, 0, len(s.files))
	for i := range s.files {
		if p := s.progress[i]; p != nil {
			files = append(files, *p)
		} else {
			files = append(files, fileProgress{path: s.files[i].RawPath, total: s.files[i].Size})
		}
	}
	logs := append([]string(nil), s.logs...)
	return s.name, files, logs
}

// totals returns aggregate byte and file counts.
func (s *tuiState) totals() (doneBytes, totalBytes int64, doneFiles, totalFiles int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	totalFiles = len(s.files)
	for i := range s.files {
		p := s.progress[i]
		if p == nil {
			continue
		}
		if p.total > 0 {
			totalBytes += p.total
		}
		doneBytes += p.done
		if p.finished {
			doneFiles++
		}
	}
	return
}

// --- reporter ---------------------------------------------------------------

type tuiReporter struct {
	st *tuiState
}

func (r *tuiReporter) Status(f string, a ...any) { r.st.log(fmt.Sprintf(f, a...)) }
func (r *tuiReporter) Info(f string, a ...any)   { r.st.log(fmt.Sprintf(f, a...)) }
func (r *tuiReporter) Warn(f string, a ...any)   { r.st.log(fmt.Sprintf(f, a...)) }
func (r *tuiReporter) Error(f string, a ...any)  { r.st.log(fmt.Sprintf(f, a...)) }

func (r *tuiReporter) TorrentName(name string)     { r.st.setName(name) }
func (r *tuiReporter) FileList(files []fileEntry)  { r.st.setFiles(files) }
func (r *tuiReporter) FileSize(index int, n int64) { r.st.setSize(index, n) }
func (r *tuiReporter) BeginFile(i int, p string, t int64, initial int64) {
	r.st.begin(i, p, t, initial)
}
func (r *tuiReporter) AdvanceFile(i int, n int64)         { r.st.advance(i, n) }
func (r *tuiReporter) EndFile(i int, o Outcome, p string) { r.st.end(i, o, p) }
func (r *tuiReporter) Finish(_, _, _ int)                 {}
func (r *tuiReporter) Close()                             {}

// --- messages ---------------------------------------------------------------

type msgTick struct{}
type msgPrepareDone struct {
	plan *downloadPlan
	err  error
}
type msgDownloadDone struct {
	outcomes   []Outcome
	downloaded int
	skipped    int
	failed     int
	failedIdx  []int
}

const (
	stateWizard = iota
	stateFetching
	stateDownloading
	stateDone
)

// --- model ------------------------------------------------------------------

type tuiModel struct {
	cfg        Config
	configPath string
	baseCtx    context.Context

	state         int
	width, height int
	ready         bool

	contentWidth int
	leftPad      int
	bodyHeight   int
	fileBarWidth int

	inputs []textinput.Model
	focus  int
	err    string

	st  *tuiState
	rep *tuiReporter

	control chan tea.Msg

	plan   *downloadPlan
	busy   bool
	paused bool
	gate   *gate
	cancel context.CancelFunc

	outcomes                    []Outcome
	failedIdx                   []int
	downloaded, skipped, failed int

	bars    []progress.Model
	overall progress.Model

	scroll       int
	spinnerFrame int

	prevBytes int64
	prevTime  time.Time
	rate      float64
}

func newTUIModel(ctx context.Context, cfg Config, configPath string) tuiModel {
	labels := []string{"Server URL", "Torrent hash", "Output directory"}
	placeholders := []string{"http://host:port", "40-char hex info hash", "."}
	values := []string{cfg.Server, cfg.Hash, cfg.Output}
	inputs := make([]textinput.Model, len(labels))
	for i := range labels {
		ti := textinput.New()
		ti.Placeholder = placeholders[i]
		ti.SetValue(values[i])
		ti.CharLimit = 512
		ti.Width = 60
		if i == 0 {
			ti.Focus()
		}
		inputs[i] = ti
	}

	st := newTUIState()
	return tuiModel{
		cfg:        cfg,
		configPath: configPath,
		baseCtx:    ctx,
		inputs:     inputs,
		st:         st,
		rep:        &tuiReporter{st: st},
		control:    make(chan tea.Msg, 8),
		overall: progress.New(
			progress.WithSolidFill(colActive),
			progress.WithoutPercentage(),
		),
		prevTime: time.Now(),
	}
}

func waitForControl(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return msgTick{} })
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, tickCmd())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case msgTick:
		m.spinnerFrame++
		m.sampleRate()
		return m, tickCmd()

	case msgPrepareDone:
		return m.onPrepareDone(msg)

	case msgDownloadDone:
		m.busy = false
		m.outcomes = msg.outcomes
		m.failedIdx = msg.failedIdx
		m.downloaded, m.skipped, m.failed = msg.downloaded, msg.skipped, msg.failed
		m.state = stateDone
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m tuiModel) onPrepareDone(msg msgPrepareDone) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.busy = false
		m.failed = 1
		m.st.log("[!] " + msg.err.Error())
		m.state = stateDone
		return m, nil
	}
	m.plan = msg.plan
	m.bars = make([]progress.Model, len(msg.plan.files))
	for i := range m.bars {
		m.bars[i] = progress.New(progress.WithSolidFill(colActive), progress.WithoutPercentage())
	}
	m.state = stateDownloading
	m.layout()

	indices := make([]int, len(msg.plan.files))
	for i := range indices {
		indices[i] = i
	}
	cmd := m.startDownload(indices)
	return m, cmd
}

func (m *tuiModel) startDownload(indices []int) tea.Cmd {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	m.gate = newGate()
	m.busy = true
	m.paused = false

	go func() {
		outcomes, d, s, f := downloadFiles(ctx, m.plan, m.cfg, indices, m.rep, m.gate)
		var failedIdx []int
		for _, i := range indices {
			if outcomes[i] == OutcomeFailed {
				failedIdx = append(failedIdx, i)
			}
		}
		m.control <- msgDownloadDone{
			outcomes: outcomes, downloaded: d, skipped: s, failed: f, failedIdx: failedIdx,
		}
	}()
	return waitForControl(m.control)
}

func (m *tuiModel) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	if m.gate != nil {
		m.gate.resume()
	}
	return m, tea.Quit
}

func (m tuiModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Esc is the one quit key that is always safe: in the wizard it is not a
	// printable character, so it never collides with typing. "q" is an alias on
	// the non-form screens, and ctrl+c always works.
	switch msg.String() {
	case "ctrl+c", "esc":
		return m.quit()
	case "q":
		if m.state != stateWizard {
			return m.quit()
		}
	}

	switch m.state {
	case stateWizard:
		return m.onWizardKey(msg)
	case stateDownloading:
		switch msg.String() {
		case "p":
			if m.busy && m.gate != nil {
				if m.paused {
					m.gate.resume()
				} else {
					m.gate.pause()
				}
				m.paused = !m.paused
			}
		case "r":
			if !m.busy && len(m.failedIdx) > 0 {
				m.state = stateDownloading
				cmd := m.startDownload(m.failedIdx)
				return m, cmd
			}
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			if m.scroll < len(m.bars)-1 {
				m.scroll++
			}
		}
	case stateDone:
		if msg.String() == "r" && !m.busy && len(m.failedIdx) > 0 {
			m.state = stateDownloading
			cmd := m.startDownload(m.failedIdx)
			return m, cmd
		}
	}
	return m, nil
}

func (m tuiModel) onWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "down":
		m.focus = (m.focus + 1) % len(m.inputs)
		m.syncFocus()
		return m, textinput.Blink
	case "shift+tab", "up":
		m.focus = (m.focus - 1 + len(m.inputs)) % len(m.inputs)
		m.syncFocus()
		return m, textinput.Blink
	case "enter":
		if m.focus < len(m.inputs)-1 {
			m.focus++
			m.syncFocus()
			return m, textinput.Blink
		}
		return m.submit()
	}

	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	if m.err != "" {
		m.err = ""
	}
	return m, cmd
}

func (m *tuiModel) syncFocus() {
	for i := range m.inputs {
		if i == m.focus {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m tuiModel) submit() (tea.Model, tea.Cmd) {
	server := strings.TrimRight(strings.TrimSpace(m.inputs[0].Value()), "/")
	hash := strings.ToUpper(strings.TrimSpace(m.inputs[1].Value()))
	output := strings.TrimSpace(m.inputs[2].Value())

	switch {
	case server == "":
		m.err = "Server URL is required"
		return m, nil
	case !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://"):
		m.err = "Server URL must start with http:// or https://"
		return m, nil
	case hash == "":
		m.err = "Torrent hash is required"
		return m, nil
	}

	m.cfg.Server = server
	m.cfg.Hash = hash
	m.cfg.Output = output
	m.cfg.normalize()
	m.err = ""
	m.persistConfig()
	m.state = stateFetching
	m.busy = true

	go func() {
		plan, err := prepareDownload(m.baseCtx, m.cfg, m.rep)
		m.control <- msgPrepareDone{plan: plan, err: err}
	}()
	return m, waitForControl(m.control)
}

// persistConfig remembers the wizard's settings so the next launch prefills
// them. Failures are reported in the log but never block the download.
func (m *tuiModel) persistConfig() {
	if m.configPath == "" {
		return
	}
	if err := SaveConfig(m.configPath, m.cfg); err != nil {
		m.st.log("[!] Could not save config: " + err.Error())
	}
}

// sampleRate computes an aggregate transfer rate from consecutive ticks.
func (m *tuiModel) sampleRate() {
	doneBytes, _, _, _ := m.st.totals()
	now := time.Now()
	elapsed := now.Sub(m.prevTime).Seconds()
	if elapsed > 0 {
		delta := doneBytes - m.prevBytes
		if delta >= 0 {
			m.rate = float64(delta) / elapsed
		}
	}
	m.prevBytes = doneBytes
	m.prevTime = now
}

func (m *tuiModel) layout() {
	if m.width == 0 {
		return
	}

	cw := m.width - leftMargin - rightMargin
	if cw < 20 {
		cw = m.width - leftMargin
	}
	if cw < 1 {
		cw = 1
	}
	m.contentWidth = cw
	m.leftPad = leftMargin

	inputWidth := cw - 8
	if inputWidth < 16 {
		inputWidth = 16
	}
	for i := range m.inputs {
		m.inputs[i].Width = inputWidth
	}

	m.fileBarWidth = clampInt(cw/3, 16, 40)
	m.overall.Width = cw
	if m.overall.Width < 16 {
		m.overall.Width = 16
	}
	for i := range m.bars {
		m.bars[i].Width = m.fileBarWidth
	}

	m.bodyHeight = m.height - footerLines
	if m.bodyHeight < 3 {
		m.bodyHeight = 3
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fileRows returns how many file rows fit in the body for the current state.
// The estimate errs high on overhead so the frame never overflows and the
// footer stays pinned.
func (m tuiModel) fileRows() int {
	overhead := 20 // header + overall + blanks + log title + log lines + overflow hints
	if m.state == stateDone {
		overhead = 24 // plus summary panel and its spacing
	}
	rows := m.bodyHeight - overhead
	if rows < 1 {
		rows = 1
	}
	return rows
}

// --- view -------------------------------------------------------------------

func (m tuiModel) View() string {
	if !m.ready {
		return "starting ruget..."
	}
	if m.width < minTermWidth || m.height < minTermHeight {
		msg := styleWarn.Render("Terminal too small — please resize")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}
	switch m.state {
	case stateWizard:
		return m.frame(m.viewWizard(), m.footer())
	case stateFetching:
		return m.frame(m.viewFetching(), m.footer())
	default:
		return m.frame(m.viewDownload(), m.footer())
	}
}

// frame constrains the body to the centered content column and pins the footer
// to the bottom row, so the status bar never floats with the content.
func (m tuiModel) frame(body, footer string) string {
	body = "\n" + body // top margin
	lines := strings.Split(body, "\n")
	if len(lines) > m.bodyHeight {
		lines = lines[:m.bodyHeight]
	}
	body = strings.Join(lines, "\n")

	bodyStyle := lipgloss.NewStyle().Width(m.contentWidth).Height(m.bodyHeight)
	container := lipgloss.NewStyle().PaddingLeft(m.leftPad)
	return container.Render(lipgloss.JoinVertical(lipgloss.Left, bodyStyle.Render(body), footer))
}

func (m tuiModel) rule() string {
	w := m.contentWidth
	if w < 1 {
		w = 1
	}
	return styleMuted.Render(strings.Repeat("─", w))
}

func (m tuiModel) viewWizard() string {
	var b strings.Builder
	b.WriteString(gradientBanner() + "\n")
	b.WriteString(styleMuted.Render("rTorrent HTTP downloader") + "\n")
	b.WriteString(styleDim.Render(fmt.Sprintf("by %s · v%s", author, version)) + "\n")
	b.WriteString(m.rule() + "\n\n")

	labels := []string{"Server URL", "Torrent hash", "Output directory"}
	for i, in := range m.inputs {
		cursor := "  "
		labelStyle := styleMuted
		if i == m.focus {
			cursor = styleAccent.Render("▸ ")
			labelStyle = lipgloss.NewStyle().Bold(true)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, labelStyle.Render(labels[i])))
		b.WriteString("  " + in.View() + "\n\n")
	}

	if m.err != "" {
		b.WriteString(styleErr.Render("  ✗ "+m.err) + "\n")
	}
	return b.String()
}

func (m tuiModel) viewFetching() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := frames[m.spinnerFrame%len(frames)]

	_, _, logs := m.st.snapshot()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s %s\n", styleAccent.Render(frame), styleTitle.Render("Fetching torrent metadata...")))
	b.WriteString(m.rule() + "\n\n")
	b.WriteString(m.renderLogArea(logs))
	return b.String()
}

// overallPercent prefers byte progress (accurate for single large files) and
// falls back to file count when sizes are unknown.
func (m tuiModel) overallPercent() float64 {
	doneBytes, totalBytes, doneFiles, totalFiles := m.st.totals()
	if totalBytes > 0 {
		p := float64(doneBytes) / float64(totalBytes)
		if p > 1 {
			return 1
		}
		if p < 0 {
			return 0
		}
		return p
	}
	if totalFiles > 0 {
		return float64(doneFiles) / float64(totalFiles)
	}
	return 0
}

func (m tuiModel) viewDownload() string {
	name, _, logs := m.st.snapshot()
	doneBytes, totalBytes, doneFiles, totalFiles := m.st.totals()

	var b strings.Builder

	// Header
	header := name
	if header == "" {
		header = "ruget"
	}
	b.WriteString(styleTitle.Render(truncate(header, m.contentWidth)) + "\n")
	b.WriteString(styleMuted.Render(truncate(fmt.Sprintf("%s  ·  %s", m.cfg.Server, m.cfg.Hash), m.contentWidth)) + "\n")
	b.WriteString(m.rule() + "\n\n")

	// Overall bar
	pct := m.overallPercent()
	info := fmt.Sprintf("%d/%d files", doneFiles, totalFiles)
	if totalBytes > 0 {
		info = fmt.Sprintf("%s / %s  ·  %s", formatBytes(doneBytes), formatBytes(totalBytes), info)
	}
	if m.rate > 0 {
		info += "  ·  " + formatBytes(int64(m.rate)) + "/s"
		if totalBytes > doneBytes {
			eta := time.Duration(float64(totalBytes-doneBytes)/m.rate) * time.Second
			info += "  ·  ETA " + formatDuration(eta)
		}
	}
	b.WriteString(styleMuted.Render("Overall") + "\n")
	b.WriteString(m.overall.ViewAs(pct) + "\n")
	b.WriteString(styleDim.Render(truncate(info, m.contentWidth)) + "\n\n")

	// File rows (windowed)
	b.WriteString(m.visibleFileRows() + "\n\n")

	// Fixed-height log area
	b.WriteString(styleMuted.Render("Log") + "\n")
	b.WriteString(m.renderLogArea(logs))

	// Summary sits just above the footer.
	if m.state == stateDone {
		b.WriteString("\n" + m.renderSummary())
	}
	return b.String()
}

func (m tuiModel) visibleFileRows() string {
	_, files, _ := m.st.snapshot()
	if len(files) == 0 {
		return styleMuted.Render("(no files)")
	}

	rows := m.fileRows()
	start := m.scroll
	if start > len(files)-1 {
		start = max(0, len(files)-rows)
	}
	if start < 0 {
		start = 0
	}
	end := min(len(files), start+rows)

	nameWidth := m.contentWidth - m.fileBarWidth - 6
	if nameWidth < 8 {
		nameWidth = 8
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		f := files[i]
		bar := ""
		if i < len(m.bars) {
			bar = m.bars[i].ViewAs(f.percent())
		}
		b.WriteString(fmt.Sprintf("%s %s %s\n",
			styleDim.Render(fmt.Sprintf("%3d", i)),
			bar,
			statusLabel(f, nameWidth),
		))
	}
	if end < len(files) {
		b.WriteString(styleDim.Render(fmt.Sprintf("↓ %d more", len(files)-end)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m tuiModel) renderSummary() string {
	if m.failed > 0 && m.downloaded == 0 && m.skipped == 0 {
		return styleErr.Render(fmt.Sprintf("Aborted: %d failed", m.failed))
	}
	border := colDone
	if m.failed > 0 {
		border = colWarn
	}
	line := fmt.Sprintf("%s downloaded · %s skipped · %s failed",
		styleDone.Render(strconv.Itoa(m.downloaded)),
		styleAccent.Render(strconv.Itoa(m.skipped)),
		styleErr.Render(strconv.Itoa(m.failed)),
	)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(border)).Padding(0, 2).Render(line)
}

// renderLogArea always returns exactly logLines lines so the frame never jumps.
func (m tuiModel) renderLogArea(logs []string) string {
	if len(logs) > logLines {
		logs = logs[len(logs)-logLines:]
	}
	lines := make([]string, 0, logLines)
	for _, l := range logs {
		lines = append(lines, m.logStyle(l).Render(truncate(l, m.contentWidth)))
	}
	for len(lines) < logLines {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) logStyle(l string) lipgloss.Style {
	switch {
	case strings.Contains(l, "[!]") && strings.Contains(l, "WARNING"):
		return styleWarn
	case strings.Contains(l, "[!]"):
		return styleErr
	case strings.Contains(l, "[✓]"), strings.Contains(l, "[↻]"):
		return styleDone
	default:
		return styleMuted
	}
}

func (m tuiModel) footer() string {
	compact := m.contentWidth < 70
	left := m.footerKeys(compact)
	right := m.footerStatus()

	// Never let the bar wrap: drop the status if the two halves collide.
	if compact || lipgloss.Width(left)+lipgloss.Width(right)+1 > m.contentWidth {
		right = ""
	}
	gap := m.contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m tuiModel) footerKeys(compact bool) string {
	hint := func(k, label string) string {
		return styleKey.Render(k) + " " + styleMuted.Render(label)
	}
	sep := styleDim.Render("   ")
	switch m.state {
	case stateWizard:
		if compact {
			return hint("enter", "next") + sep + hint("esc", "quit")
		}
		return hint("enter", "next") + sep + hint("tab", "move") + sep + hint("esc", "quit")
	case stateFetching:
		return hint("esc", "quit")
	case stateDownloading:
		if m.busy {
			label := "pause"
			if m.paused {
				label = "resume"
			}
			keys := hint("p", label)
			if !compact {
				keys += sep + hint("↑↓", "scroll")
			}
			keys += sep + hint("esc", "quit")
			if len(m.failedIdx) > 0 {
				keys += sep + hint("r", "retry")
			}
			return keys
		}
		return hint("r", "retry") + sep + hint("esc", "quit")
	case stateDone:
		if len(m.failedIdx) > 0 {
			if compact {
				return hint("r", "retry") + sep + hint("esc", "quit")
			}
			return hint("r", "retry failed") + sep + hint("esc", "quit")
		}
		return hint("esc", "quit")
	}
	return hint("esc", "quit")
}

func (m tuiModel) footerStatus() string {
	switch m.state {
	case stateWizard:
		return styleMuted.Render("setup")
	case stateFetching:
		return styleTitle.Render("fetching metadata")
	case stateDownloading:
		if m.paused {
			return styleWarn.Render("● paused")
		}
		pct := int(m.overallPercent() * 100)
		return styleTitle.Render(fmt.Sprintf("downloading %d%%", pct))
	case stateDone:
		if m.failed > 0 {
			return styleErr.Render("done with failures")
		}
		return styleDone.Render("complete")
	}
	return ""
}

func statusLabel(f fileProgress, width int) string {
	name := truncate(f.path, width)
	switch {
	case f.finished && f.outcome == OutcomeFailed:
		return styleErr.Render("✗ " + name)
	case f.finished && f.outcome == OutcomeSkipped:
		return styleAccent.Render("= " + name)
	case f.finished:
		return styleDone.Render("✓ " + name)
	case f.started:
		return styleTitle.Render("↓ " + name)
	default:
		return styleMuted.Render("· " + name)
	}
}

// --- banner -----------------------------------------------------------------

func gradientBanner() string {
	word := "RUGET"
	palette := []string{colActive, colAccent, colPink}
	runes := []rune(word)
	var b strings.Builder
	for i, ch := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		b.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(lerpHex(palette, t))).
			Render(string(ch)))
	}
	return b.String()
}

// lerpHex interpolates across a hex palette for a position 0..1.
func lerpHex(palette []string, t float64) string {
	if len(palette) == 0 {
		return "#FFFFFF"
	}
	if len(palette) == 1 {
		return palette[0]
	}
	segments := len(palette) - 1
	pos := t * float64(segments)
	idx := int(pos)
	if idx >= segments {
		idx = segments - 1
		pos = float64(segments)
	}
	local := pos - float64(idx)

	r1, g1, b1 := hexToRGB(palette[idx])
	r2, g2, b2 := hexToRGB(palette[idx+1])
	lerp := func(a, b int) int { return a + int(float64(b-a)*local) }
	return fmt.Sprintf("#%02X%02X%02X", lerp(r1, r2), lerp(g1, g2), lerp(b1, b2))
}

func hexToRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	r, _ := strconv.ParseInt(hex[0:2], 16, 32)
	g, _ := strconv.ParseInt(hex[2:4], 16, 32)
	b, _ := strconv.ParseInt(hex[4:6], 16, 32)
	return int(r), int(g), int(b)
}

// --- entry point ------------------------------------------------------------

func runTUI(ctx context.Context, cfg Config, configPath string) int {
	model := newTUIModel(ctx, cfg, configPath)
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mm, ok := final.(tuiModel); ok && mm.failed > 0 {
		return 1
	}
	return 0
}
