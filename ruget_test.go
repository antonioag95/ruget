package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHash = "ABCDEF0123456789"
	testName = "My Torrent"
)

var testFiles = map[string][]byte{
	"0": []byte(strings.Repeat("A", 10000)),
	"1": []byte(strings.Repeat("B", 5000)),
}

// mockRTorrent returns a server mimicking the ruTorrent httprpc + data endpoints.
func mockRTorrent(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/plugins/httprpc/action.php", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "mode=list") {
			fmt.Fprintf(w, `{"t":{"%s":[0,1,2,3,"%s",6]}}`, testHash, testName)
			return
		}
		io.WriteString(w, `[["folder/file0.bin"],["folder/file1.bin"]]`)
	})

	mux.HandleFunc("/plugins/data/action.php", func(w http.ResponseWriter, r *http.Request) {
		data, ok := testFiles[r.URL.Query().Get("no")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		start := 0
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			start, _ = strconv.Atoi(strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)[0])
		}
		if start >= len(data) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		chunk := data[start:]
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		w.Write(chunk)
	})

	return httptest.NewServer(mux)
}

func testConfig(dir string) Config {
	cfg := DefaultConfig()
	cfg.Server = ""
	cfg.Hash = testHash
	cfg.Output = dir
	cfg.Jobs = 1
	cfg.Retries = 1
	cfg.Probe = true
	return cfg
}

func discardReporter() *ConsoleReporter {
	return NewConsoleReporter(io.Discard, false)
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a<b>c:d"e/f\g|h?i*j`: "a_b_c_d_e_f_g_h_i_j",
		"":                    "_",
		".":                   "_",
		"..":                  "_",
		"normal.txt":          "normal.txt",
		"  spaced  ":          "spaced",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeFormOrder(t *testing.T) {
	got := encodeForm([][2]string{{"mode", "list"}, {"cmd", "d.throttle_name="}})
	want := "mode=list&cmd=d.throttle_name%3D"
	if got != want {
		t.Fatalf("encodeForm = %q, want %q", got, want)
	}
}

func TestDownloadLifecycle(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Server = srv.URL
	rep := discardReporter()
	ctx := context.Background()

	plan, err := prepareDownload(ctx, cfg, rep)
	if err != nil {
		t.Fatalf("prepareDownload: %v", err)
	}
	if plan.name != testName {
		t.Fatalf("name = %q, want %q", plan.name, testName)
	}
	if len(plan.files) != 2 {
		t.Fatalf("files = %d, want 2", len(plan.files))
	}
	if plan.files[0].Size != int64(len(testFiles["0"])) {
		t.Fatalf("probed size = %d, want %d", plan.files[0].Size, len(testFiles["0"]))
	}

	indices := []int{0, 1}
	if _, d, s, f := downloadFiles(ctx, plan, cfg, indices, rep, newGate()); d != 2 || s != 0 || f != 0 {
		t.Fatalf("first run = d%d s%d f%d, want d2", d, s, f)
	}
	for _, name := range []string{"file0.bin", "file1.bin"} {
		if _, err := os.Stat(filepath.Join(plan.baseDir, "folder", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	// Second run: everything already complete -> skipped via 416.
	if _, d, s, f := downloadFiles(ctx, plan, cfg, indices, rep, newGate()); d != 0 || s != 2 || f != 0 {
		t.Fatalf("second run = d%d s%d f%d, want s2", d, s, f)
	}

	// Truncate file 1 and verify resume via 206.
	partial := filepath.Join(plan.baseDir, "folder", "file1.bin")
	if err := os.WriteFile(partial, testFiles["1"][:3000], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, d, s, f := downloadFiles(ctx, plan, cfg, indices, rep, newGate()); d != 1 || s != 1 || f != 0 {
		t.Fatalf("resume run = d%d s%d f%d, want d1 s1", d, s, f)
	}
	got, _ := os.ReadFile(partial)
	if string(got) != string(testFiles["1"]) {
		t.Fatalf("resumed content mismatch")
	}
}

func TestInterruptThenResume(t *testing.T) {
	content := bytes.Repeat([]byte("Z"), 2_000_000)

	mux := http.NewServeMux()
	mux.HandleFunc("/plugins/httprpc/action.php", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "mode=list") {
			fmt.Fprintf(w, `{"t":{"%s":[0,1,2,3,"%s",6]}}`, testHash, testName)
			return
		}
		io.WriteString(w, `[["big.bin"]]`)
	})
	mux.HandleFunc("/plugins/data/action.php", func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			start, _ = strconv.Atoi(strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)[0])
		}
		chunk := content[start:]
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
		}
		flusher, _ := w.(http.Flusher)
		const step = 4096
		for i := 0; i < len(chunk); i += step {
			end := i + step
			if end > len(chunk) {
				end = len(chunk)
			}
			w.Write(chunk[i:end])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Server = srv.URL
	cfg.Probe = false
	cfg.Retries = 0

	// First attempt: interrupt mid-transfer.
	plan, err := prepareDownload(context.Background(), cfg, discardReporter())
	if err != nil {
		t.Fatalf("prepareDownload: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	downloadFiles(ctx, plan, cfg, []int{0}, discardReporter(), newGate())

	path := filepath.Join(plan.baseDir, "big.bin")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("partial file missing: %v", err)
	}
	if info.Size() == 0 || info.Size() >= int64(len(content)) {
		t.Fatalf("expected a partial file, got size=%d full=%d", info.Size(), len(content))
	}

	// Second run, same hash/output: must resume and finish.
	plan2, err := prepareDownload(context.Background(), cfg, discardReporter())
	if err != nil {
		t.Fatalf("prepareDownload (2): %v", err)
	}
	if _, d, s, f := downloadFiles(context.Background(), plan2, cfg, []int{0}, discardReporter(), newGate()); d != 1 || s != 0 || f != 0 {
		t.Fatalf("resume run = d%d s%d f%d, want d1", d, s, f)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after resume: %d/%d bytes", len(got), len(content))
	}
}

func TestBadHash(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	cfg := testConfig(t.TempDir())
	cfg.Server = srv.URL
	cfg.Hash = "DEADBEEF"
	if _, err := prepareDownload(context.Background(), cfg, discardReporter()); err == nil {
		t.Fatal("expected error for unknown hash")
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var attempts int32
	mux := http.NewServeMux()
	mux.HandleFunc("/plugins/httprpc/action.php", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "mode=list") {
			fmt.Fprintf(w, `{"t":{"%s":[0,1,2,3,"%s",6]}}`, testHash, testName)
			return
		}
		io.WriteString(w, `[["only.bin"]]`)
	})
	mux.HandleFunc("/plugins/data/action.php", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", "4")
		w.Write([]byte("done"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Server = srv.URL
	cfg.Retries = 3
	cfg.Probe = false

	plan, err := prepareDownload(context.Background(), cfg, discardReporter())
	if err != nil {
		t.Fatalf("prepareDownload: %v", err)
	}
	if _, d, _, f := downloadFiles(context.Background(), plan, cfg, []int{0}, discardReporter(), newGate()); d != 1 || f != 0 {
		t.Fatalf("retry run = d%d f%d, want d1", d, f)
	}
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Fatalf("attempts = %d, want >= 2", got)
	}
}

func TestConcurrentDownloads(t *testing.T) {
	srv := mockRTorrent(t)
	defer srv.Close()

	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Server = srv.URL
	cfg.Jobs = 4
	cfg.Probe = false

	plan, err := prepareDownload(context.Background(), cfg, discardReporter())
	if err != nil {
		t.Fatalf("prepareDownload: %v", err)
	}
	if _, d, s, f := downloadFiles(context.Background(), plan, cfg, []int{0, 1}, discardReporter(), newGate()); d != 2 || s != 0 || f != 0 {
		t.Fatalf("concurrent run = d%d s%d f%d, want d2", d, s, f)
	}
}
