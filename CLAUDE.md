# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build -o mistystep .   # build binary (static/ assets are embedded at compile time)
go run .                  # run without building
go vet ./...              # lint
```

The service runs as a user-level systemd unit:

```bash
systemctl --user restart mistystep   # restart after rebuilding
systemctl --user status mistystep
```

There are no tests.

## Architecture

Three files make up the entire codebase:

- **`main.go`** — HTTP server, WebSocket hub, file/clipboard pollers, mDNS
- **`clipboard.go`** — thin wrappers around `wl-paste`/`wl-copy` (Wayland) and `xclip` (X11); detection is via `WAYLAND_DISPLAY` / `DISPLAY` env vars
- **`static/`** — embedded via `//go:embed static` into the binary at build time; served as a plain `http.FileServer`

### Data flow

Two background goroutines poll for changes and broadcast JSON over WebSocket to all connected clients:

- `pollClipboard` — reads clipboard every `--clip-poll` (default 1 s), hashes content with FNV-64a, broadcasts `{type:"clipboard", content:"..."}` only on change
- `pollFiles` — reads `--dir` (default `~/Downloads`) every `--file-poll` (default 3 s), hashes the file list, broadcasts `{type:"files", files:[...]}` only on change

On WebSocket connect (`handleWS`), the current clipboard and file list are pushed immediately so the client doesn't wait for the next poll cycle.

### WebSocket hub

`Hub` is a classic Go channel-based broadcast hub: `register`, `unregister`, and `broadcast` channels are processed in a single `run()` goroutine to avoid lock contention. Slow clients are dropped rather than back-pressured.

### Frontend (`static/`)

Single-page vanilla JS app (`app.js` + `style.css` + `index.html`). Two tabs:

- **Send to Linux** — textarea → `POST /api/clipboard`; drop zone / file picker → `POST /api/upload` with XHR progress
- **Get from Linux** — live clipboard display + file list from WebSocket; each file row has a share button that fetches the file as a blob and calls `navigator.share({files:[...]})` for iOS (falls back to blob-URL download on desktop)

XSS prevention: file names are always passed through `escapeHtml()`/`escapeAttr()` before insertion into `innerHTML`; clipboard text uses `textContent` only.

### Key design constraints

- **No authentication** — intentional, trusted-LAN only
- **No database** — state is the filesystem and the live clipboard
- **Static assets are compiled in** — any change to `static/` requires a rebuild and service restart to take effect
- **Clipboard requires a display server** — the systemd unit must have `WAYLAND_DISPLAY` and `XDG_RUNTIME_DIR` set in its `Environment=` to read/write the Wayland clipboard
