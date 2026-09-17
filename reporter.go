package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Reporter is the presentation sink shared by the CLI and TUI front-ends.
// Implementations must be safe for concurrent use: downloads run on multiple
// goroutines when jobs > 1.
type Reporter interface {
	Status(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)

	TorrentName(name string)
	FileList(files []fileEntry)
	FileSize(index int, size int64)

	BeginFile(index int, displayPath string, total int64, initial int64)
	AdvanceFile(index int, n int64)
	EndFile(index int, outcome Outcome, displayPath string)
	Finish(downloaded, skipped, failed int)

	Close()
}

// isTerminal reports whether f is an interactive character device.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ConsoleReporter is the plain-text front-end. With a TTY it renders a single
// aggregate progress line; otherwise it stays quiet between per-file messages.
type ConsoleReporter struct {
	out   io.Writer
	isTTY bool

	mu         sync.Mutex
	totalFiles int
	doneFiles  int
	doneBytes  int64
	totalBytes int64
	sized      map[int]bool
	fileDone   map[int]int64
	start      time.Time
	lastDraw   time.Time
	drewLine   bool
}

// NewConsoleReporter builds a reporter writing to w.
func NewConsoleReporter(w io.Writer, tty bool) *ConsoleReporter {
	return &ConsoleReporter{
		out:      w,
		isTTY:    tty,
		sized:    make(map[int]bool),
		fileDone: make(map[int]int64),
		start:    time.Now(),
	}
}

func (r *ConsoleReporter) print(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drewLine {
		fmt.Fprint(r.out, "\r\033[K")
		r.drewLine = false
	}
	fmt.Fprintf(r.out, format+"\n", args...)
}

func (r *ConsoleReporter) Status(format string, args ...any) { r.print(format, args...) }
func (r *ConsoleReporter) Info(format string, args ...any)   { r.print(format, args...) }
func (r *ConsoleReporter) Warn(format string, args ...any)   { r.print(format, args...) }
func (r *ConsoleReporter) Error(format string, args ...any)  { r.print(format, args...) }

func (r *ConsoleReporter) TorrentName(name string) {
	r.print("✅ Found torrent name: '%s'", name)
}

func (r *ConsoleReporter) FileList(files []fileEntry) {
	r.mu.Lock()
	r.totalFiles = len(files)
	r.mu.Unlock()
	r.print("📁 Found %d file(s).", len(files))
}

func (r *ConsoleReporter) FileSize(_ int, _ int64) {
	// Sizes only matter for the TUI table; the CLI learns them as it downloads.
}

func (r *ConsoleReporter) addTotal(index int, size int64) {
	if size <= 0 {
		return
	}
	if !r.sized[index] {
		r.sized[index] = true
		r.totalBytes += size
	}
}

func (r *ConsoleReporter) BeginFile(index int, _ string, total int64, initial int64) {
	r.mu.Lock()
	r.addTotal(index, total)
	// Adjust the aggregate by the delta so retries (which re-report the bytes
	// already on disk) are not double counted.
	r.doneBytes += initial - r.fileDone[index]
	r.fileDone[index] = initial
	r.mu.Unlock()
}

func (r *ConsoleReporter) AdvanceFile(index int, n int64) {
	r.mu.Lock()
	r.fileDone[index] += n
	r.doneBytes += n
	draw := r.isTTY && time.Since(r.lastDraw) >= 100*time.Millisecond
	if draw {
		r.lastDraw = time.Now()
		r.drawProgressLocked()
	}
	r.mu.Unlock()
}

func (r *ConsoleReporter) EndFile(_ int, outcome Outcome, displayPath string) {
	switch outcome {
	case OutcomeSkipped:
		r.print("  [=] Already complete, skipping: %s", displayPath)
	case OutcomeResumed:
		r.print("  [↻] Resumed: %s", displayPath)
	case OutcomeDownloaded:
		r.print("  [✓] Downloaded: %s", displayPath)
	}
}

func (r *ConsoleReporter) Finish(downloaded, skipped, failed int) {
	r.mu.Lock()
	r.doneFiles = downloaded + skipped + failed
	r.drawProgressLocked()
	if r.drewLine {
		fmt.Fprintln(r.out)
		r.drewLine = false
	}
	r.mu.Unlock()
	r.print("\n🎉 All downloads finished: %d downloaded, %d skipped, %d failed.", downloaded, skipped, failed)
}

func (r *ConsoleReporter) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drewLine {
		fmt.Fprintln(r.out)
		r.drewLine = false
	}
}

func (r *ConsoleReporter) drawProgressLocked() {
	if !r.isTTY {
		return
	}
	elapsed := time.Since(r.start).Seconds()
	rate := float64(0)
	if elapsed > 0 {
		rate = float64(r.doneBytes) / elapsed
	}
	line := fmt.Sprintf(
		"  [%d/%d] %s / %s  %s/s",
		r.doneFiles, r.totalFiles,
		formatBytes(r.doneBytes), formatBytes(r.totalBytes),
		formatBytes(int64(rate)),
	)
	if rate > 0 && r.totalBytes > r.doneBytes {
		remaining := time.Duration(float64(r.totalBytes-r.doneBytes)/rate) * time.Second
		line += fmt.Sprintf("  ETA %s", formatDuration(remaining))
	}
	fmt.Fprintf(r.out, "\r\033[K%s", line)
	r.drewLine = true
}

// formatBytes renders a byte count using binary units.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), units[exp])
}

// formatDuration renders a duration as HH:MM:SS without sub-second noise.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, m, s := total/3600, (total%3600)/60, total%60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// truncate shortens s to at most n runes, prefixing an ellipsis when clipped.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return "..." + string(runes[len(runes)-(n-3):])
}
