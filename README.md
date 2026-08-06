# ferryman

> A single-purpose SSH port forwarder for your desktop.

Ferryman binds arbitrary local ports to arbitrary ports on a remote machine, with a small
GUI to toggle each crossing on and off. It runs on the *host* side (Windows / macOS / Linux)
and exists to replace the port-forwarding half of a heavy editor remote extension **without
running any agent on the remote** — the remote cost is just the existing `sshd`.

The name: a ferryman carries traffic between two shores and nothing else. Each rule is one
crossing, `local_addr → remote_addr`.

## Why it's built this way

- **In-process SSH** (`golang.org/x/crypto/ssh`), not shelling out to the `ssh` binary.
  One codebase → a single static binary per OS, and no dependency on `ControlMaster`
  (which native Windows OpenSSH does not implement).
- **`remote_addr` is resolved on the remote side.** So a dev server bound only to the
  remote loopback (`127.0.0.1` or `::1`) is reachable without exposing it on `0.0.0.0`.
- **Auto-reconnect** with exponential backoff (1s → 30s) plus a 20s keepalive; enabled
  forwards are re-established on reconnect.
- **Auth reuse:** ssh-agent first (Windows OpenSSH agent named pipe / `SSH_AUTH_SOCK`
  elsewhere), then key files; host, user, port and IdentityFile are read from
  `~/.ssh/config`.
- **known_hosts TOFU:** unknown hosts prompt then get pinned; a *changed* key is refused
  (possible MITM).

## Layout

    forwarder/            engine (no GUI deps; cgo-free; cross-compiles anywhere)
      forwarder.go        Manager: connection, forwards, reconnect
      sshconn.go          ssh_config resolution, agent+key auth, known_hosts
      agent_unix.go       agent via SSH_AUTH_SOCK        (build tag: !windows)
      agent_windows.go    agent via \\.\pipe\openssh-ssh-agent (build tag: windows)
      store.go            config.json load/save
    cmd/ferryman/       Fyne GUI front-end (needs a C toolchain; Fyne v2.6+)
    cmd/ferryman-cli/   headless CLI front-end (cgo-free)

## Install

Each release ships one asset per OS (see [Releases](../../releases)):

    ferryman_<tag>_darwin_arm64.app.zip   macOS (Apple Silicon) — a .app bundle
    ferryman_<tag>_linux_amd64
    ferryman_<tag>_windows_amd64.exe

**macOS.** Unzip and move `Ferryman.app` to `/Applications`. The bundle is only
*ad-hoc* signed — there is no Apple Developer ID behind this project, so it is not
notarized either — and Gatekeeper refuses a downloaded copy until the quarantine
attribute is cleared:

    xattr -dr com.apple.quarantine /Applications/Ferryman.app

Or, without the terminal: try to open it, then allow it under
System Settings → Privacy & Security → "Open Anyway".

## Build

First fetch deps:

    go mod tidy

GUI — the primary binary `ferryman` (Fyne needs cgo + a C toolchain + OpenGL —
build it natively on each OS):

    go build -o ferryman ./cmd/ferryman
    # or, for a proper .app / .exe bundle (icon: cmd/ferryman/Icon.png):
    go install fyne.io/tools/cmd/fyne@latest
    fyne package --src ./cmd/ferryman --name Ferryman --app-id com.unasuke.ferryman

On macOS, `script/package-macos.sh` builds exactly what a release ships — the
`.app`, ad-hoc signed, zipped with `ditto`. The release workflow runs this same
script, so you can check a bundle locally before tagging:

    script/package-macos.sh v1.2.3     # -> dist/ferryman_v1.2.3_darwin_arm64.app.zip
    open build/macos/Ferryman.app      # the bundle it zipped

macOS needs the Xcode command-line tools; Windows needs a gcc (e.g. MSYS2/mingw-w64).

On Windows a plain `go build` produces a console-subsystem binary, so an extra
command-prompt window opens alongside the GUI. Build `ferryman.exe` with the
`windowsgui` subsystem to suppress it:

    go build -ldflags "-H windowsgui" -o ferryman.exe ./cmd/ferryman

`fyne package` already targets the GUI subsystem, so this flag is only needed
for a bare `go build` on Windows.

CLI — `ferryman-cli` (cgo-free, cross-compiles trivially):

    go build -o ferryman-cli ./cmd/ferryman-cli
    GOOS=windows GOARCH=amd64 go build -o ferryman-cli.exe ./cmd/ferryman-cli
    GOOS=darwin  GOARCH=arm64 go build -o ferryman-cli     ./cmd/ferryman-cli

## Config

`config.json` lives in the per-user config dir
(`%AppData%\ferryman\` on Windows, `~/Library/Application Support/ferryman/` on macOS).
The GUI writes it for you; the CLI reads it.

    {
      "host": "myserver",
      "rules": [
        { "name": "web",   "local_addr": "127.0.0.1:8080", "remote_addr": "localhost:3000", "enabled": true },
        { "name": "api",   "local_addr": "127.0.0.1:8081", "remote_addr": "127.0.0.1:8000", "enabled": true },
        { "name": "vite6", "local_addr": "127.0.0.1:5173", "remote_addr": "[::1]:5173",     "enabled": false }
      ]
    }

- `host` is an `~/.ssh/config` alias (preferred) or a hostname. Optional overrides:
  `"user"`, `"port"`, `"identity_files": [...]`, `"known_hosts"`.
- `local_addr` binds on the host; use `0.0.0.0:PORT` to expose on the LAN (mind the firewall).
- `remote_addr` is resolved on the remote; use `[::1]:PORT` for an IPv6-only remote service.
- In the GUI you can type just a port: a bare `8080` becomes `127.0.0.1:8080` (local) or
  `localhost:8080` (remote). Enter a full `host:port` when you need a specific host,
  `[::1]:PORT`, or `0.0.0.0:PORT`.

## Testing

    go test ./forwarder/          # engine tests (cgo-free, run everywhere)
    go test -race ./forwarder/    # recommended, but needs cgo + a C toolchain
    go test ./cmd/ferryman/       # GUI helper tests; needs gcc (cgo)

The `forwarder` tests are self-contained: the end-to-end test stands up an
in-process SSH server and bridges a `direct-tcpip` channel to a local echo
server, so nothing outside loopback is touched. `-race` and the GUI tests need a
C toolchain and are therefore skipped in cgo-free cross builds.

## Notes / next steps

- **Load keys into ssh-agent** so no passphrase prompt is needed
  (`Start-Service ssh-agent; ssh-add` on Windows). The agent is tried first, before any
  key file.
- **The CLI passphrase prompt echoes.** To stay dependency-free and cgo-free, `ferryman`
  reads the passphrase as a plain line, so it is visible on the terminal. Use ssh-agent
  (above) to avoid the prompt entirely; the GUI uses a masked password field instead.
- A menu-bar/tray-only variant is easy to add on top of the same engine
  (e.g. `fyne.io/systray`) if you'd rather not keep a window open.
- **Port suggestions (GUI).** While connected, the GUI polls `ss` over the same SSH
  connection and, when a *new* remote listener appears, offers to forward it — you
  Add or Dismiss it; nothing is tunneled automatically. It suggests only ports that
  show up after you connect and aren't already forwarded. Linux remotes only (needs
  `ss`); it stays quiet if `ss` isn't available. The headless CLI never runs it.
