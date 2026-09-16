# mistystep

Local network file and clipboard sharing between your Linux machine and any device on the same LAN (iPhone, iPad, Android, another laptop — anything with a browser).

No apps to install on the remote device. No cloud. No accounts.

## What it does

| Direction | What you can do |
|-----------|----------------|
| Phone → Linux | Type or paste text → lands in Linux clipboard |
| Phone → Linux | Pick or drop a file → saved to a directory on Linux |
| Linux → Phone | Linux clipboard mirrors live to the browser (updates within 1 s) |
| Linux → Phone | Browse and download any file from the served directory |

Everything updates live via WebSocket — no manual refresh needed.

## Requirements

- Go 1.22+
- Linux with either:
  - **Wayland**: `wl-clipboard` (`sudo apt install wl-clipboard`)
  - **X11**: `xclip` (`sudo apt install xclip`)
- **GNOME on Wayland**: install `xclip` too. GNOME lacks the data-control protocol, so `wl-paste` grabs keyboard focus on every clipboard poll (focus jumps out of your browser, a generic icon flashes in the dock). When `DISPLAY` is set and `xclip` is available, mistystep reads the clipboard through Xwayland instead, which never takes focus.

## Install

```bash
git clone git@github.com:alimate/mistystep.git
cd mistystep
go build -o mistystep .
```

Or run directly without building:

```bash
go run .
```

## Usage

```bash
./mistystep
```

Then open the printed URL from any device on the same network:

```
dir:          /home/alice/Downloads
clip-poll:    1s   file-poll: 3s   max-upload: 500 MiB
listening on  :7070
local:        http://localhost:7070
network:      http://192.168.1.42:7070
mdns:         http://future.local:7070
```

On iPhone/iPad, `http://future.local:7070` (your machine's hostname + `.local`) is the easiest to type. Add it to your Home Screen for one-tap access.

## Configuration

All options are CLI flags with sensible defaults:

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `7070` | HTTP listen port |
| `--bind` | *(all interfaces)* | Listen address — set to `127.0.0.1` to restrict to localhost |
| `--dir` | `~/Downloads` | Directory for uploads and the file browser |
| `--clip-poll` | `1s` | How often to check the Linux clipboard for changes |
| `--file-poll` | `3s` | How often to check the directory for new/removed files |
| `--max-upload` | `500` | Max upload size in MiB |
| `--mdns-name` | `mistystep` | mDNS service instance name (visible in Bonjour discovery) |
| `--mdns-host` | *(os.Hostname())* | Hostname used in mDNS — override if you want a custom `.local` name |
| `--no-mdns` | `false` | Disable mDNS registration entirely |

Examples:

```bash
# Share ~/Desktop/shared on port 80
./mistystep --dir ~/Desktop/shared --port 80

# Faster clipboard sync, larger uploads
./mistystep --clip-poll 500ms --max-upload 2000

# Custom .local name (requires our mDNS server to win UDP 5353 over avahi)
./mistystep --mdns-host mybox
# → http://mybox.local:7070

# Disable mDNS entirely (e.g. on a network where multicast is blocked)
./mistystep --no-mdns
```

## Accessing without the port number

By default the server listens on port 7070. To drop the port from the URL:

**Redirect port 80 → 7070 with iptables** (no root needed for the app itself):

```bash
sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 7070
sudo iptables -t nat -A OUTPUT -p tcp -d localhost --dport 80 -j REDIRECT --to-port 7070
# persist across reboots:
sudo apt install iptables-persistent && sudo netfilter-persistent save
```

Or grant the binary permission to bind low ports directly:

```bash
sudo setcap 'cap_net_bind_service=+ep' ./mistystep
./mistystep --port 80
```

## Custom `.local` hostname via avahi

If `avahi-daemon` is running (common on Ubuntu/Debian), it already holds UDP 5353 so mistystep's built-in mDNS server falls back gracefully. avahi already advertises your machine's hostname (`future.local` or whatever `hostname` returns).

To add an additional alias (e.g. `mistystep.local`) through avahi:

```bash
# one-shot, foreground
avahi-publish --address -R mistystep.local $(hostname -I | awk '{print $1}')
```

Persist it with a systemd service:

```bash
sudo tee /etc/systemd/system/mdns-alias-mistystep.service <<EOF
[Unit]
Description=mDNS alias mistystep.local
After=avahi-daemon.service
Requires=avahi-daemon.service

[Service]
ExecStart=/usr/bin/avahi-publish --address -R mistystep.local $(hostname -I | awk '{print $1}')
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable --now mdns-alias-mistystep
```

## Run as a systemd service

To have mistystep start automatically at boot:

```bash
sudo tee /etc/systemd/system/mistystep.service <<EOF
[Unit]
Description=mistystep local sharing
After=network.target

[Service]
ExecStart=/home/$USER/mistystep/mistystep --dir /home/$USER/Downloads
Restart=on-failure
User=$USER
Environment=WAYLAND_DISPLAY=wayland-0
Environment=DISPLAY=:0
Environment=XDG_RUNTIME_DIR=/run/user/$(id -u)

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable --now mistystep
```

> **Note:** The `WAYLAND_DISPLAY`, `DISPLAY` and `XDG_RUNTIME_DIR` environment variables are needed so the service can access the clipboard (`DISPLAY` lets it read via `xclip`, which avoids the GNOME focus-stealing issue above). Adjust the values to match your session (`echo $WAYLAND_DISPLAY $DISPLAY $XDG_RUNTIME_DIR` in a terminal).

## Security

mistystep has **no authentication**. Anyone on the same network can:

- Read and write your clipboard
- Browse and download files from the served directory
- Upload files into that directory

This is intentional for a trusted LAN. Do not run it on public or untrusted networks, and do not forward its port through your router to the internet.
