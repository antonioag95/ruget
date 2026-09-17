package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// printTorrents writes one "name<TAB>hash" line per torrent, suitable for
// piping into other tools.
func printTorrents(w io.Writer, entries []torrentEntry) {
	for _, e := range entries {
		name := e.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(w, "%s\t%s\n", name, e.Hash)
	}
}

// promptTorrent lists the server's torrents and reads a 1-based selection from
// in, setting cfg.Hash to the chosen torrent. It is used by the plain CLI when
// no hash was supplied.
func promptTorrent(ctx context.Context, cfg *Config, in io.Reader, out io.Writer) error {
	entries, err := listTorrents(ctx, newSession(), cfg.Server)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no torrents found on %s", cfg.Server)
	}

	fmt.Fprintf(out, "Available torrents on %s:\n", cfg.Server)
	for i, e := range entries {
		name := e.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(out, "  %2d) %s (%s)\n", i+1, name, e.Hash)
	}
	fmt.Fprintf(out, "Select a torrent [1-%d]: ", len(entries))

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("reading selection: %w", err)
	}
	raw := strings.TrimSpace(line)
	choice, err := strconv.Atoi(raw)
	if err != nil || choice < 1 || choice > len(entries) {
		return fmt.Errorf("invalid selection %q", raw)
	}

	chosen := entries[choice-1]
	cfg.Hash = chosen.Hash
	fmt.Fprintf(out, "Selected: %s\n", chosen.Hash)
	return nil
}
