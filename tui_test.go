package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIFullFlow(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	cfg := testConfig(t.TempDir())
	cfg.Server = srv.URL
	cfg.Probe = false

	m := newTUIModel(context.Background(), cfg, "")
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := tm.(tuiModel)

	mm.inputs[0].SetValue(srv.URL + "/#")
	mm.inputs[1].SetValue(testHash)
	mm.inputs[2].SetValue(cfg.Output)

	tm, _ = mm.submit()
	mm = tm.(tuiModel)
	if mm.state != stateFetching {
		t.Fatalf("state = %d, want fetching", mm.state)
	}

	deadline := time.After(10 * time.Second)
	for mm.state != stateDone {
		select {
		case msg := <-mm.control:
			tm, _ = mm.Update(msg)
			mm = tm.(tuiModel)
		case <-deadline:
			t.Fatalf("timed out in state %d", mm.state)
		}
		if view := mm.View(); view == "" {
			t.Fatal("empty view")
		}
	}

	if mm.downloaded != 2 || mm.failed != 0 {
		t.Fatalf("downloaded=%d failed=%d, want 2/0", mm.downloaded, mm.failed)
	}
	if !strings.Contains(mm.View(), "downloaded") {
		t.Fatalf("summary missing from view:\n%s", mm.View())
	}
}

func TestTUIViewsDoNotPanic(t *testing.T) {
	cfg := DefaultConfig()
	m := newTUIModel(context.Background(), cfg, "")
	for _, size := range []tea.WindowSizeMsg{
		{Width: 40, Height: 10},
		{Width: 120, Height: 50},
		{Width: 0, Height: 0},
	} {
		var tm tea.Model = m
		tm, _ = tm.Update(size)
		mm := tm.(tuiModel)
		_ = mm.View()

		// Exercise every state's view.
		mm.state = stateFetching
		_ = mm.View()
		mm.state = stateDownloading
		_ = mm.View()
		mm.state = stateDone
		_ = mm.View()
	}
}

func TestTUIPauseAndRetryKeys(t *testing.T) {
	cfg := DefaultConfig()
	m := newTUIModel(context.Background(), cfg, "")
	m.state = stateDownloading
	m.busy = true
	m.gate = newGate()

	tm, _ := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm := tm.(tuiModel)
	if !mm.paused {
		t.Fatal("expected paused after p")
	}
	tm, _ = mm.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm = tm.(tuiModel)
	if mm.paused {
		t.Fatal("expected resumed after second p")
	}
}

func TestTUIPrepareStartsDownload(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	cfg := testConfig(t.TempDir())
	cfg.Server = srv.URL
	cfg.Probe = false

	m := newTUIModel(context.Background(), cfg, "")
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := tm.(tuiModel)
	mm.inputs[0].SetValue(srv.URL + "/#")
	mm.inputs[1].SetValue(testHash)
	mm.inputs[2].SetValue(cfg.Output)

	tm, _ = mm.submit()
	mm = tm.(tuiModel)

	select {
	case msg := <-mm.control:
		tm, _ = mm.Update(msg)
		mm = tm.(tuiModel)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for prepare")
	}

	if mm.state != stateDownloading {
		t.Fatalf("state = %d, want downloading", mm.state)
	}
	if !mm.busy || mm.gate == nil {
		t.Fatalf("busy=%v gate=%v, want both set", mm.busy, mm.gate)
	}

	select {
	case <-mm.control:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for download completion")
	}
}

func TestDefaultConfigHasNoEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server != "" || cfg.Hash != "" {
		t.Fatalf("defaults should be empty, got server=%q hash=%q", cfg.Server, cfg.Hash)
	}
}

func TestWizardRequiresInput(t *testing.T) {
	cfg := DefaultConfig()
	m := newTUIModel(context.Background(), cfg, "")
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := tm.(tuiModel)

	if mm.inputs[0].Value() != "" || mm.inputs[1].Value() != "" {
		t.Fatalf("server/hash should start blank, got %q/%q", mm.inputs[0].Value(), mm.inputs[1].Value())
	}

	tm, _ = mm.submit()
	mm = tm.(tuiModel)
	if mm.state != stateWizard {
		t.Fatalf("state = %d, want wizard", mm.state)
	}
	if mm.err == "" {
		t.Fatal("expected a validation error")
	}

	mm.inputs[0].SetValue("ftp://nope")
	tm, _ = mm.submit()
	mm = tm.(tuiModel)
	if !strings.Contains(mm.err, "http") {
		t.Fatalf("expected scheme error, got %q", mm.err)
	}
}

func TestTUIPrefillFromConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Server = "http://example:8080"
	cfg.Hash = "ABCDEF"
	m := newTUIModel(context.Background(), cfg, "")
	if m.inputs[0].Value() != cfg.Server || m.inputs[1].Value() != cfg.Hash {
		t.Fatalf("expected prefill from config, got %q/%q", m.inputs[0].Value(), m.inputs[1].Value())
	}
}

func TestFramePinsFooter(t *testing.T) {
	for _, state := range []int{stateWizard, stateFetching, stateDownloading, stateDone} {
		cfg := DefaultConfig()
		m := newTUIModel(context.Background(), cfg, "")
		var tm tea.Model = m
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		mm := tm.(tuiModel)
		mm.state = state
		mm.busy = state == stateDownloading
		mm.gate = newGate()

		out := mm.View()
		lines := strings.Split(out, "\n")
		if len(lines) != 40 {
			t.Fatalf("state %d: view lines = %d, want 40", state, len(lines))
		}
		last := strings.TrimSpace(lines[len(lines)-1])
		if !strings.Contains(last, "quit") {
			t.Fatalf("state %d: footer not pinned, last line = %q", state, last)
		}
	}
}

func TestTUISubmitPersistsConfig(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "ruget.json")
	cfg := testConfig(t.TempDir())
	cfg.Server = srv.URL
	cfg.Hash = testHash

	m := newTUIModel(context.Background(), cfg, cfgPath)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := tm.(tuiModel)
	mm.inputs[0].SetValue(srv.URL + "/#")
	mm.inputs[1].SetValue(testHash)
	mm.inputs[2].SetValue(cfg.Output)

	tm, _ = mm.submit()
	mm = tm.(tuiModel)

	select {
	case <-mm.control:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for prepare")
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Server != srv.URL || loaded.Output != cfg.Output {
		t.Fatalf("persisted config = %+v", loaded)
	}
	if loaded.Hash != "" {
		t.Fatalf("hash must not be persisted, got %q", loaded.Hash)
	}

	// A fresh model must prefill the saved server/output, but never the hash.
	reloaded := newTUIModel(context.Background(), loaded, cfgPath)
	if reloaded.inputs[0].Value() != srv.URL || reloaded.inputs[2].Value() != cfg.Output {
		t.Fatalf("prefill = %q/%q", reloaded.inputs[0].Value(), reloaded.inputs[2].Value())
	}
	if reloaded.inputs[1].Value() != "" {
		t.Fatalf("hash prefill = %q, want empty", reloaded.inputs[1].Value())
	}
}

func TestOverallPercentUsesBytes(t *testing.T) {
	cfg := DefaultConfig()
	m := newTUIModel(context.Background(), cfg, "")
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	mm := tm.(tuiModel)

	mm.st.setFiles([]fileEntry{{RawPath: "movie.mkv", Size: 800}})
	// A single file actively downloading: file count is still 0/1, but bytes
	// are known, so the overall bar must reflect byte progress.
	mm.st.begin(0, "movie.mkv", 800, 0)
	mm.st.advance(0, 200)
	mm.state = stateDownloading
	mm.busy = true
	mm.gate = newGate()

	if pct := mm.overallPercent(); pct < 0.24 || pct > 0.26 {
		t.Fatalf("overallPercent = %v, want ~0.25", pct)
	}
	if !strings.Contains(mm.View(), "25%") {
		t.Fatalf("footer should show byte-based percent:\n%s", mm.View())
	}
}

func TestCleanBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://host:8081/#":     "http://host:8081",
		"http://host:8081/#/":    "http://host:8081",
		"http://host:8081/":      "http://host:8081",
		"http://host:8081":       "http://host:8081",
		"https://host/base/#":    "https://host/base",
		"http://host:8081/?x=1":  "http://host:8081",
		"  http://host:8081/#  ": "http://host:8081",
		"notaurl":                "notaurl",
	}
	for in, want := range cases {
		if got := cleanBaseURL(in); got != want {
			t.Errorf("cleanBaseURL(%q) = %q, want %q", in, got, want)
		}
	}

	cfg := Config{Server: "http://host:8081/#", Hash: " abc "}
	cfg.normalize()
	if cfg.Server != "http://host:8081" || cfg.Hash != "ABC" {
		t.Fatalf("normalize = %q/%q", cfg.Server, cfg.Hash)
	}
}

func TestVersionCredits(t *testing.T) {
	if !strings.Contains(fullVersion(), version) {
		t.Fatalf("fullVersion() = %q, missing version", fullVersion())
	}
	if !strings.Contains(fullVersion(), appName) {
		t.Fatalf("fullVersion() = %q, missing app name", fullVersion())
	}
	if author != "antonioag95" {
		t.Fatalf("author = %q", author)
	}

	cfg := DefaultConfig()
	m := newTUIModel(context.Background(), cfg, "")
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if !strings.Contains(tm.(tuiModel).View(), "antonioag95") {
		t.Fatalf("wizard view is missing the author credit:\n%s", tm.(tuiModel).View())
	}
}

// isQuit reports whether a command is the Bubble Tea quit command.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestQuitKeysConsistent(t *testing.T) {
	states := []int{stateWizard, stateFetching, stateDownloading, stateDone}
	for _, st := range states {
		m := newTUIModel(context.Background(), DefaultConfig(), "")
		m.state = st

		if _, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyEsc}); !isQuit(cmd) {
			t.Fatalf("state %d: esc must quit", st)
		}
		if _, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuit(cmd) {
			t.Fatalf("state %d: ctrl+c must quit", st)
		}
	}

	// "q" must quit on the dashboard...
	m := newTUIModel(context.Background(), DefaultConfig(), "")
	m.state = stateDownloading
	if _, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); !isQuit(cmd) {
		t.Fatal("state downloading: q must quit")
	}

	// ...but must be typed into the wizard's focused field, not quit.
	m = newTUIModel(context.Background(), DefaultConfig(), "")
	m.state = stateWizard
	_, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isQuit(cmd) {
		t.Fatal("wizard: q must be typed, not quit")
	}
}

func TestTUIBeginSeedsResumeProgress(t *testing.T) {
	st := newTUIState()
	rep := &tuiReporter{st: st}
	rep.FileList([]fileEntry{{RawPath: "f.bin", Size: 1000}})
	// Resuming: 400 bytes already on disk must seed the bar, not reset it to 0.
	rep.BeginFile(0, "f.bin", 1000, 400)

	_, files, _ := st.snapshot()
	if p := files[0].percent(); p < 0.39 || p > 0.41 {
		t.Fatalf("resumed percent = %v, want ~0.4", p)
	}
	doneBytes, totalBytes, _, _ := st.totals()
	if doneBytes != 400 || totalBytes != 1000 {
		t.Fatalf("totals = %d/%d, want 400/1000", doneBytes, totalBytes)
	}
}

func TestTUIReporterState(t *testing.T) {
	st := newTUIState()
	rep := &tuiReporter{st: st}

	rep.TorrentName("Torrent")
	rep.FileList([]fileEntry{{RawPath: "a/b.bin"}, {RawPath: "c.bin", Size: 100}})
	rep.FileSize(0, 50)
	rep.BeginFile(0, "a/b.bin", 50, 0)
	rep.AdvanceFile(0, 50)
	rep.EndFile(0, OutcomeDownloaded, "a/b.bin")
	rep.EndFile(1, OutcomeSkipped, "c.bin")

	name, files, _ := st.snapshot()
	if name != "Torrent" {
		t.Fatalf("name = %q", name)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	if files[0].percent() != 1 {
		t.Fatalf("file0 percent = %v, want 1", files[0].percent())
	}
	if files[1].percent() != 1 {
		t.Fatalf("skipped percent = %v, want 1", files[1].percent())
	}

	doneBytes, totalBytes, doneFiles, totalFiles := st.totals()
	if doneBytes != 150 || totalBytes != 150 || doneFiles != 2 || totalFiles != 2 {
		t.Fatalf("totals = %d/%d %d/%d", doneBytes, totalBytes, doneFiles, totalFiles)
	}
}

func TestGradientHelpers(t *testing.T) {
	if got := lerpHex([]string{"#000000", "#FFFFFF"}, 0); got != "#000000" {
		t.Fatalf("lerp 0 = %s", got)
	}
	if got := lerpHex([]string{"#000000", "#FFFFFF"}, 1); got != "#FFFFFF" {
		t.Fatalf("lerp 1 = %s", got)
	}
	if got := lerpHex([]string{"#000000", "#FFFFFF"}, 0.5); got != "#7F7F7F" && got != "#808080" {
		t.Fatalf("lerp 0.5 = %s", got)
	}
	if gradientBanner() == "" {
		t.Fatal("empty banner")
	}
}

func TestConsoleReporterFinish(t *testing.T) {
	rep := NewConsoleReporter(io.Discard, false)
	rep.TorrentName("T")
	rep.FileList(make([]fileEntry, 3))
	rep.BeginFile(0, "x", 10, 0)
	rep.AdvanceFile(0, 10)
	rep.EndFile(0, OutcomeDownloaded, "x")
	rep.Finish(1, 1, 1)
	rep.Close()
}

func TestMain(t *testing.T) {
	// Smoke-test flag dispatch without touching the network.
	if code := run([]string{"--save-config", "--config", t.TempDir() + "/cfg.json"}); code != 0 {
		t.Fatalf("save-config exit = %d", code)
	}
	if _, err := os.Stat(t.TempDir()); err != nil {
		_ = err
	}
}
