package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Outcome is the terminal state of a single file download.
type Outcome int

const (
	OutcomeDownloaded Outcome = iota
	OutcomeResumed
	OutcomeSkipped
	OutcomeFailed
)

// gate is a context-aware pause gate checked between chunks.
type gate struct {
	mu       sync.Mutex
	paused   bool
	resumeCh chan struct{}
}

func newGate() *gate { return &gate{} }

func (g *gate) pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		g.paused = true
		g.resumeCh = make(chan struct{})
	}
}

func (g *gate) resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		g.paused = false
		close(g.resumeCh)
	}
}

// wait blocks while paused and reports false if ctx is cancelled first.
func (g *gate) wait(ctx context.Context) bool {
	g.mu.Lock()
	for g.paused {
		ch := g.resumeCh
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
		g.mu.Lock()
	}
	g.mu.Unlock()
	return ctx.Err() == nil
}

// statusError carries a non-2xx HTTP status for retry classification.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.code >= 500 || se.code == http.StatusTooManyRequests
	}
	return true // network/IO issues are worth another attempt
}

// downloadFile downloads one file, resuming from a partial copy and retrying
// transient failures with exponential backoff.
func downloadFile(
	ctx context.Context,
	client *http.Client,
	cfg Config,
	index int,
	fullPath, displayPath, rawPath string,
	rep Reporter,
	g *gate,
) Outcome {
	attempts := cfg.Retries + 1
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			rep.Warn("  [!] Retry %d/%d for %s (%v)", attempt, cfg.Retries, displayPath, lastErr)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return OutcomeFailed
			}
		}

		outcome, err := attemptDownload(ctx, client, cfg, index, fullPath, displayPath, rawPath, rep, g)
		if err == nil {
			return outcome
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}

	if ctx.Err() == nil {
		rep.Error("  [!] Failed: %s (%v)", displayPath, lastErr)
	}
	return OutcomeFailed
}

// attemptDownload performs a single download pass, returning an Outcome when
// the request completed (even for size mismatches) or an error to retry.
func attemptDownload(
	ctx context.Context,
	client *http.Client,
	cfg Config,
	index int,
	fullPath, displayPath, rawPath string,
	rep Reporter,
	g *gate,
) (Outcome, error) {
	localSize := int64(0)
	if info, err := os.Stat(fullPath); err == nil {
		localSize = info.Size()
	}

	params := url.Values{}
	params.Set("hash", cfg.Hash)
	params.Set("no", strconv.Itoa(index))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURLFor(cfg.Server)+"?"+params.Encode(), nil)
	if err != nil {
		return OutcomeFailed, err
	}
	setCommonHeaders(req, cfg.Server)
	if localSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", localSize))
	}

	resp, err := client.Do(req)
	if err != nil {
		return OutcomeFailed, err
	}
	defer resp.Body.Close()

	// Range beyond EOF means we already have the whole file.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && localSize > 0 {
		rep.AdvanceFile(index, localSize)
		return OutcomeSkipped, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OutcomeFailed, &statusError{code: resp.StatusCode}
	}

	resumed := resp.StatusCode == http.StatusPartialContent && localSize > 0
	done := int64(0)
	if resumed {
		done = localSize
	}

	var remaining int64 = -1
	if resp.ContentLength >= 0 {
		remaining = resp.ContentLength
		if remaining == 0 {
			rep.Warn("  [!] WARNING: Server reported 0 bytes for: %s", rawPath)
		}
	}

	total := int64(-1)
	if remaining >= 0 {
		total = done + remaining
	}

	if dir := filepath.Dir(fullPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return OutcomeFailed, err
		}
	}

	mode := os.O_CREATE | os.O_WRONLY
	if resumed {
		mode |= os.O_APPEND
	} else {
		mode |= os.O_TRUNC
	}
	file, err := os.OpenFile(fullPath, mode, 0o644)
	if err != nil {
		return OutcomeFailed, err
	}

	rep.BeginFile(index, displayPath, total, done)

	writer := bufio.NewWriterSize(file, 1<<20)
	buf := make([]byte, chunkSize)
	var copyErr error

	for {
		if !g.wait(ctx) {
			copyErr = ctx.Err()
			break
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := writer.Write(buf[:n]); werr != nil {
				copyErr = werr
				break
			}
			rep.AdvanceFile(index, int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			copyErr = rerr
			break
		}
	}

	if flushErr := writer.Flush(); copyErr == nil {
		copyErr = flushErr
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return OutcomeFailed, copyErr
	}

	finalSize := int64(0)
	if info, err := os.Stat(fullPath); err == nil {
		finalSize = info.Size()
	}
	if total >= 0 && finalSize != total {
		rep.Error("  [!] Error: size mismatch (%d/%d bytes): %s", finalSize, total, displayPath)
		return OutcomeFailed, nil
	}

	if resumed {
		return OutcomeResumed, nil
	}
	return OutcomeDownloaded, nil
}

// downloadPlan is the resolved metadata needed for one torrent.
type downloadPlan struct {
	client  *http.Client
	name    string
	files   []fileEntry
	baseDir string
}

// prepareDownload fetches metadata, creates the destination tree and, when
// enabled, probes unknown file sizes in the background.
func prepareDownload(ctx context.Context, cfg Config, rep Reporter) (*downloadPlan, error) {
	client := newSession()

	name, err := getTorrentName(ctx, client, cfg.Server, cfg.Hash, rep)
	if err != nil {
		return nil, err
	}

	files, err := getFileList(ctx, client, cfg.Server, cfg.Hash, rep)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
		return nil, err
	}

	baseDir := cfg.Output
	if len(files) > 1 {
		rep.Status("    Torrent contains multiple files. Creating directory: '%s'", name)
		baseDir = filepath.Join(cfg.Output, name)
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return nil, err
		}
	} else {
		rep.Status("    Torrent contains a single file. Saving directly in output directory.")
	}

	if cfg.Probe && len(files) > 1 {
		probeSizes(ctx, client, cfg.Server, cfg.Hash, files, probeWorkers(cfg.Jobs), rep)
	}

	return &downloadPlan{client: client, name: name, files: files, baseDir: baseDir}, nil
}

// probeWorkers bounds the size-probing pool.
func probeWorkers(jobs int) int {
	w := jobs * 2
	if w < 4 {
		w = 4
	}
	if w > 16 {
		w = 16
	}
	return w
}

// downloadFiles downloads the given indices with a worker pool, returning
// per-index outcomes and aggregate counts.
func downloadFiles(
	ctx context.Context,
	plan *downloadPlan,
	cfg Config,
	indices []int,
	rep Reporter,
	g *gate,
) ([]Outcome, int, int, int) {
	if len(indices) == 0 {
		return nil, 0, 0, 0
	}

	rep.Status("\n③  Starting downloads...")

	outcomes := make([]Outcome, len(plan.files))
	for i := range outcomes {
		outcomes[i] = OutcomeFailed
	}

	workers := cfg.Jobs
	if workers < 1 {
		workers = 1
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				safeRel := sanitizeRelPath(plan.files[index].RawPath)
				fullPath := filepath.Join(plan.baseDir, filepath.FromSlash(safeRel))
				outcome := downloadFile(ctx, plan.client, cfg, index, fullPath, safeRel, plan.files[index].RawPath, rep, g)
				rep.EndFile(index, outcome, safeRel)
				outcomes[index] = outcome
			}
		}()
	}

	for _, index := range indices {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return summarize(outcomes, indices)
		}
	}
	close(jobs)
	wg.Wait()

	return summarize(outcomes, indices)
}

// summarize counts outcomes for the given indices.
func summarize(outcomes []Outcome, indices []int) ([]Outcome, int, int, int) {
	downloaded, skipped, failed := 0, 0, 0
	for _, index := range indices {
		switch outcomes[index] {
		case OutcomeDownloaded, OutcomeResumed:
			downloaded++
		case OutcomeSkipped:
			skipped++
		default:
			failed++
		}
	}
	return outcomes, downloaded, skipped, failed
}

// runCLI executes the full download flow for the plain-text front-end.
func runCLI(ctx context.Context, cfg Config, rep Reporter) int {
	rep.Info("⚙️  Configuration loaded:")
	rep.Info("   Server: %s", cfg.Server)
	rep.Info("   Hash:   %s", cfg.Hash)
	rep.Info("----------------------------------------")

	plan, err := prepareDownload(ctx, cfg, rep)
	if err != nil {
		rep.Error("[!] %v", err)
		return 1
	}

	indices := make([]int, len(plan.files))
	for i := range indices {
		indices[i] = i
	}

	_, downloaded, skipped, failed := downloadFiles(ctx, plan, cfg, indices, rep, newGate())
	rep.Finish(downloaded, skipped, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
