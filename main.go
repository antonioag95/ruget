package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses arguments, merges configuration and dispatches to a front-end.
func run(args []string) int {
	fs := flag.NewFlagSet("ruget", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "ruget - rTorrent HTTP downloader\nby %s · %s\n\nUsage:\n  ruget [flags]\n\nFlags:\n", author, fullVersion())
		fs.PrintDefaults()
	}

	var (
		url         string
		hash        string
		output      string
		cli         bool
		configPath  string
		jobs        int
		retries     int
		noProbe     bool
		dumpFLS     bool
		saveConfig  bool
		showVersion bool
	)

	fs.StringVar(&url, "u", "", "Base server URL")
	fs.StringVar(&url, "url", "", "Base server URL")
	fs.StringVar(&hash, "H", "", "Torrent hash")
	fs.StringVar(&hash, "hash", "", "Torrent hash")
	fs.StringVar(&output, "o", "", "Destination directory")
	fs.StringVar(&output, "output", "", "Destination directory")
	fs.BoolVar(&cli, "cli", false, "Use the plain command-line interface instead of the TUI")
	fs.StringVar(&configPath, "config", "", "Path to config file (default: ruget.json next to the executable)")
	fs.IntVar(&jobs, "j", 0, "Concurrent downloads")
	fs.IntVar(&jobs, "jobs", 0, "Concurrent downloads")
	fs.IntVar(&retries, "retries", -1, "Retry attempts for transient failures")
	fs.BoolVar(&noProbe, "no-probe", false, "Do not probe file sizes before downloading")
	fs.BoolVar(&dumpFLS, "dump-fls", false, "Print the raw file-list response and exit")
	fs.BoolVar(&saveConfig, "save-config", false, "Save merged settings to the config file and exit")
	fs.BoolVar(&showVersion, "version", false, "Print version and author, then exit")
	fs.BoolVar(&showVersion, "v", false, "Print version and author, then exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if showVersion {
		fmt.Printf("%s\nby %s\n", fullVersion(), author)
		return 0
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Load the config file (missing file is fine).
	if configPath == "" {
		if p, err := ConfigPath(); err == nil {
			configPath = p
		}
	}
	cfg := DefaultConfig()
	if configPath != "" {
		loaded, err := LoadConfig(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[!] Ignoring config %s: %v\n", configPath, err)
		} else {
			cfg = loaded
		}
	}

	// Flags override the file; config overrides built-in defaults.
	if set["u"] || set["url"] {
		cfg.Server = url
	}
	if set["H"] || set["hash"] {
		cfg.Hash = hash
	}
	if set["o"] || set["output"] {
		cfg.Output = output
	}
	if set["j"] || set["jobs"] {
		cfg.Jobs = jobs
	}
	if set["retries"] {
		cfg.Retries = retries
	}
	if noProbe {
		cfg.Probe = false
	}
	cfg.normalize()

	if saveConfig {
		if configPath == "" {
			fmt.Fprintln(os.Stderr, "[!] No config path available")
			return 1
		}
		if err := SaveConfig(configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "[!] Could not save config: %v\n", err)
			return 1
		}
		fmt.Printf("Saved config to %s\n", configPath)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	configHint := configPath
	if configHint == "" {
		configHint = "ruget.json"
	}

	if dumpFLS {
		if missing := cfg.validateEndpoint(); missing != "" {
			fmt.Fprintf(os.Stderr, "[!] %s. Pass -u/-H or edit %s.\n", missing, configHint)
			return 2
		}
		client := newSession()
		out, err := dumpFileList(ctx, client, cfg.Server, cfg.Hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[!] %v\n", err)
			return 1
		}
		fmt.Println(out)
		return 0
	}

	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	if cli || !interactive {
		if missing := cfg.validateEndpoint(); missing != "" {
			fmt.Fprintf(os.Stderr, "[!] %s. Pass -u/-H or edit %s.\n", missing, configHint)
			return 2
		}
		enableVirtualTerminal()
		rep := NewConsoleReporter(os.Stdout, isTerminal(os.Stdout))
		return runCLI(ctx, cfg, rep)
	}
	return runTUI(ctx, cfg, configPath)
}
