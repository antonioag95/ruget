# ruget

A fast, resumable downloader for torrents hosted on [ruTorrent](https://github.com/Novik/ruTorrent)
over its `httprpc` and `data` plugins. It talks to a ruTorrent instance, fetches the
file list for a torrent hash, then downloads each file directly — resuming partial
files and retrying transient failures.

`ruget` ships as a single static binary with two front-ends:

- **TUI** (default when run interactively) — a full-screen interface with a setup
  wizard, per-file progress, live logs, pause/resume and retry.
- **CLI** (`-cli`, or automatically when stdin/stdout is not a terminal) — a plain
  progress-line interface suitable for scripts and redirected output.

## Install

### Prebuilt binaries

Download the archive for your platform from the
[Releases](https://github.com/antonioag95/ruget/releases) page:

| Platform | Architecture |
| --- | --- |
| Windows | amd64 (`ruget-windows-amd64.exe`) |
| Linux | amd64, arm64 |
| macOS | amd64, arm64 |

### With Go

```sh
go install github.com/antonioag95/ruget@latest
```

Requires Go 1.24 or newer.

## Usage

```sh
ruget -u http://host:8081 -H <torrent-hash> -o ./downloads
```

Run with no arguments on a terminal to launch the interactive wizard, which also
lets you save the server URL to `ruget.json`.

Don't know the torrent hash? Point `-u` at your server and list what's loaded:

```sh
ruget -u http://host:8081 -list          # prints "name<TAB>hash" and exits
ruget -u http://host:8081                # interactive: pick a torrent from a menu
```

In the TUI wizard, leave the hash field and press `ctrl+r` to browse the server's
torrents with the arrow keys and pick one — no hash typing required. Press
`ctrl+f` (or `/`) to filter the list by name or hash; `esc` clears the filter.

### Flags

| Flag | Description |
| --- | --- |
| `-u`, `-url` | Base server URL (e.g. `http://host:8081`) |
| `-H`, `-hash` | Torrent hash (40-char info hash) |
| `-o`, `-output` | Destination directory (default `.`) |
| `-j`, `-jobs` | Concurrent downloads (default `1`) |
| `-retries` | Retry attempts for transient failures (default `3`) |
| `-no-probe` | Skip probing file sizes before downloading |
| `-cli` | Force the plain CLI instead of the TUI |
| `-config` | Path to config file (default: `ruget.json` next to the executable) |
| `-save-config` | Save the merged settings to the config file and exit |
| `-list` | List the server's torrents as `name<TAB>hash` and exit |
| `-dump-fls` | Print the raw file-list response and exit (diagnostic) |
| `-v`, `-version` | Print version and author, then exit |

### TUI keys

| Key | Action |
| --- | --- |
| `ctrl+r` | (Wizard) List torrents on the server and pick one |
| `ctrl+f` / `/` | (Picker) Filter torrents by name or hash |
| `↑` / `k`, `↓` / `j` | Move the selection / scroll the file list |
| `enter` | (Picker) Apply filter, then select the highlighted torrent |
| `r` | Retry failed files / refresh the torrent list |
| `p` | Pause / resume |
| `q`, `esc`, `ctrl+c` | Quit (`esc` goes back in the torrent picker) |

## Configuration

Settings are merged with the precedence **flags > config file > built-in defaults**.
The config file is JSON and lives next to the executable as `ruget.json`:

```json
{
  "server": "http://host:8081",
  "output": ".",
  "jobs": 3,
  "retries": 3,
  "probe": true
}
```

The torrent `hash` is intentionally **not** persisted — it identifies a single
download, not a setting, so it is supplied per run via `-H` or the wizard.

## Behavior

- **Resume**: partial files are resumed with HTTP `Range` requests. A `416` response
  means the local file is already complete, and it is skipped.
- **Retries**: transient failures (network/IO errors, `5xx`, `429`) are retried with
  exponential backoff up to `-retries` times.
- **Size probing**: unless `-no-probe` is set, `ruget` issues a cheap 1-byte ranged
  request per file to learn its size for accurate progress and ETA.
- **Names**: file and folder names from the server are sanitized before touching the
  local filesystem.

## Security

- **TLS verification is disabled** (`InsecureSkipVerify`) for compatibility with
  self-signed certificates on trusted LAN/self-hosted servers. Do **not** point
  `ruget` at an untrusted host over a public network.
- The torrent hash is never written to disk.

## Build

Cross-compile every target into `dist/` with the PowerShell script:

```powershell
.\build.ps1                     # version read from version.go
.\build.ps1 -Version 1.2.3      # override the stamped version
.\build.ps1 -Clean              # wipe dist first
.\build.ps1 -Only linux/amd64   # build a single target
```

If script execution is blocked, run:

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1
```

To build for the current platform only:

```sh
go build -o ruget .
```

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
```

CI runs these on every push and pull request; tagging `v*` builds and publishes the
cross-platform binaries to a GitHub Release.

## License

Released under the [MIT License](LICENSE).
