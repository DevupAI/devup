# devup

Run Linux commands and containers from macOS via a lightweight Lima VM. Containers require Linux kernel features (namespaces, cgroups), so on macOS devup uses a Lima VM to provide a Linux environment.

## macOS Quickstart

```bash
# Install Lima (required)
brew install lima

# Start the devup VM
devup vm up

# Run a command inside the VM
devup run -- echo hello
devup run -- ls -la /mnt/host
```

## Commands

| Command | Description |
|---------|-------------|
| `devup vm up` | Start the Lima VM and agent |
| `devup vm down` | Stop the VM |
| `devup vm shell` | Open a shell in the VM |
| `devup vm status` | Show VM and agent status |
| `devup vm logs` | Show agent logs |
| `devup vm reset-token` | Regenerate auth token (restart VM after) |
| `devup run -- <cmd> [args...]` | Run a command inside the VM |
| `devup run --mount .:/workspace -- <cmd>` | Run with project mounted at /workspace |

## Workspace Mounts (V1)

Mount your project into the VM so commands can access your files. **V1 restriction:** projects must be under your home directory (`~`).

```bash
# Smoke test: run a Python script from your project
mkdir -p ~/devup-demo && echo 'print("hi")' > ~/devup-demo/app.py
cd ~/devup-demo
devup run --mount .:/workspace -- python3 /workspace/app.py
# Output: hi

# List your project files
devup run --mount .:/workspace -- bash -lc "ls -la /workspace"

# Run Go tests (if Go is installed in VM)
devup run --mount .:/workspace -- go test ./...
```

Format: `--mount hostPath:guestPath` (e.g. `.:/workspace`). The host path is resolved relative to your current directory and must be under `~/`.

## Build

```bash
go build -o devup ./cmd/devup
```

The agent is built automatically when you run `devup vm up` (for the correct architecture: arm64 on M1/M2/M3, amd64 on Intel).

## Platform Support

- **macOS**: Full support via Lima VM
- **Linux**: Planned (run agent directly)
- **Windows**: Planned (via WSL2)

## Troubleshooting

- `limactl list` — Check if the `devup` instance exists and its status
- `limactl shell devup` — Open a shell to debug
- `curl http://127.0.0.1:7777/health` — Check if the agent is reachable (requires `X-Devup-Token` header)
- `devup vm logs` — View agent logs

---

# Toy Container (Linux only)

This repo also contains a toy container runtime built from scratch in Go (for learning). It uses namespaces and cgroups, and mounts a tmpfs isolated from the host filesystem.

**Requires Linux.** The container runtime is in `main.go` (build tag: `linux`).

## What it does

*Before starting, unzip `ubuntu_fs.zip` to create the `ubuntu_fs` directory as the container root.*

```bash
sudo su
go run main.go run /bin/bash
```

It will:
- Fork with `CLONE_NEWUTS`, `CLONE_NEWPID`, `CLONE_NEWNS` (isolated hostname, processes, mounts)
- Create a cgroup to limit memory (~1MB)
- Chroot into `./ubuntu_fs`
- Mount `/proc` and a tmpfs
- Execute the command inside the isolated environment

## Sources

- [Building Containers from Scratch with Go](https://www.safaribooksonline.com/videos/building-containers-from/9781491988404) — Liz Rice
- [GOTO 2018 • Containers From Scratch](https://www.youtube.com/watch?v=8fi7uSYlOdc)
- [sysdevbd](https://sysdevbd.com/)

## VS Code

For cross-platform development, set `GOOS=linux` in `go.toolsEnvVars` so Linux-specific code is recognized.
