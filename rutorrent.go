package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// fileEntry is one row of the torrent's file list.
type fileEntry struct {
	RawPath string // server-side relative path (slash separated)
	Size    int64  // total bytes, or -1 when unknown
}

// encodeForm builds an application/x-www-form-urlencoded body while preserving
// the exact pair order ruTorrent expects (url.Values would sort the keys).
func encodeForm(pairs [][2]string) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p[0]))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p[1]))
	}
	return b.String()
}

// listURLFor returns the httprpc endpoint for a base URL.
func listURLFor(base string) string { return base + "/plugins/httprpc/action.php" }

// downloadURLFor returns the data endpoint for a base URL.
func downloadURLFor(base string) string { return base + "/plugins/data/action.php" }

// apiPost performs an httprpc POST with the shared headers and a bounded timeout.
func apiPost(ctx context.Context, client *http.Client, base, body string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, listURLFor(base), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	setCommonHeaders(req, base)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// getTorrentName fetches and sanitizes the display name of the torrent.
func getTorrentName(ctx context.Context, client *http.Client, base, hash string, rep Reporter) (string, error) {
	rep.Status("①  Fetching torrent name...")

	pairs := [][2]string{{"mode", "list"}}
	for _, cmd := range listCmds {
		pairs = append(pairs, [2]string{"cmd", cmd})
	}

	data, err := apiPost(ctx, client, base, encodeForm(pairs))
	if err != nil {
		return "", fmt.Errorf("fetching torrent list: %w", err)
	}

	var payload struct {
		T map[string][]json.RawMessage `json:"t"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decoding torrent list: %w", err)
	}

	raw, ok := payload.T[hash]
	if !ok || len(raw) <= nameIndex {
		return "", fmt.Errorf("torrent with hash %s not found in the list", hash)
	}

	var name string
	if err := json.Unmarshal(raw[nameIndex], &name); err != nil || name == "" {
		return "", fmt.Errorf("torrent with hash %s not found in the list", hash)
	}

	safe := sanitizeName(name)
	rep.TorrentName(safe)
	return safe, nil
}

// getFileList fetches the file list for the torrent.
func getFileList(ctx context.Context, client *http.Client, base, hash string, rep Reporter) ([]fileEntry, error) {
	rep.Status("\n②  Fetching file list for the torrent...")

	body := encodeForm([][2]string{
		{"mode", "fls"},
		{"hash", hash},
		{"cmd", flsCmd},
	})

	data, err := apiPost(ctx, client, base, body)
	if err != nil {
		return nil, fmt.Errorf("fetching file list: %w", err)
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decoding file list: %w", err)
	}

	files := make([]fileEntry, 0, len(rows))
	for _, row := range rows {
		if len(row) <= pathIndex {
			continue
		}
		var rawPath string
		if err := json.Unmarshal(row[pathIndex], &rawPath); err != nil {
			continue
		}
		files = append(files, fileEntry{RawPath: rawPath, Size: -1})
	}

	rep.FileList(files)
	return files, nil
}

// probeSize discovers a file's total size with a cheap 1-byte ranged request.
// Servers that ignore Range still reveal the size via Content-Length.
func probeSize(ctx context.Context, client *http.Client, base, hash string, index int) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	params := url.Values{}
	params.Set("hash", hash)
	params.Set("no", strconv.Itoa(index))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURLFor(base)+"?"+params.Encode(), nil)
	if err != nil {
		return -1, err
	}
	setCommonHeaders(req, base)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))

	if resp.StatusCode == http.StatusPartialContent {
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			return total, nil
		}
		return -1, fmt.Errorf("unparsable Content-Range")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength >= 0 {
		return resp.ContentLength, nil
	}
	return -1, fmt.Errorf("HTTP %s", resp.Status)
}

// parseContentRangeTotal extracts the total from "bytes 0-0/12345".
func parseContentRangeTotal(value string) (int64, bool) {
	slash := strings.LastIndex(value, "/")
	if slash < 0 {
		return 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(value[slash+1:]), 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

// probeSizes resolves sizes for all files with unknown size, bounded by workers,
// reporting each result as it arrives. Probe failures are silently skipped.
func probeSizes(ctx context.Context, client *http.Client, base, hash string, files []fileEntry, workers int, rep Reporter) {
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				size, err := probeSize(ctx, client, base, hash, index)
				if err != nil {
					continue
				}
				files[index].Size = size
				rep.FileSize(index, size)
			}
		}()
	}

	for index := range files {
		if files[index].Size >= 0 {
			continue
		}
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

// dumpFileList is a diagnostic: it prints the raw "fls" response so the size
// column (if any) can be identified without guessing.
func dumpFileList(ctx context.Context, client *http.Client, base, hash string) (string, error) {
	body := encodeForm([][2]string{
		{"mode", "fls"},
		{"hash", hash},
		{"cmd", flsCmd},
	})
	data, err := apiPost(ctx, client, base, body)
	if err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return string(data), nil
	}
	return pretty.String(), nil
}
