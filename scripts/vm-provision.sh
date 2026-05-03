#!/bin/bash
# DevUp VM provision script: install base toolchains (Node, Python, Go, Ruby, Java, Rust, PHP, C/C++).
# Idempotent: safe to run on fresh VM or existing VM.
# Used by: Lima YAML provisioning (vm/lima/devup.yaml, inline) and devup vm provision (copy+run).
# If you change this script, update the embedded logic in vm/lima/devup.yaml to stay in sync.

set -e
export DEBIAN_FRONTEND=noninteractive

# Fast path: if all core tools exist, print summary and exit (no apt)
if command -v node &>/dev/null && command -v python3 &>/dev/null && command -v go &>/dev/null && command -v gcc &>/dev/null && command -v ruby &>/dev/null && command -v java &>/dev/null && command -v cargo &>/dev/null && command -v php &>/dev/null; then
  echo "=== Toolchains (already installed) ==="
  command -v node &>/dev/null && echo "node: $(node -v 2>/dev/null || echo '?')" || true
  command -v npm &>/dev/null && echo "npm: $(npm -v 2>/dev/null || echo '?')" || true
  command -v python3 &>/dev/null && echo "python3: $(python3 -V 2>&1 || echo '?')" || true
  command -v pip &>/dev/null && echo "pip: $(pip -V 2>/dev/null | head -1 || echo '?')" || command -v pip3 &>/dev/null && echo "pip: $(pip3 -V 2>/dev/null | head -1 || echo '?')" || true
  command -v go &>/dev/null && echo "go: $(go version 2>/dev/null || echo '?')" || true
  command -v ruby &>/dev/null && echo "ruby: $(ruby -v 2>/dev/null || echo '?')" || true
  command -v java &>/dev/null && echo "java: $(java -version 2>&1 | head -1 || echo '?')" || true
  command -v cargo &>/dev/null && echo "cargo: $(cargo -V 2>/dev/null || echo '?')" || true
  command -v rustc &>/dev/null && echo "rustc: $(rustc -V 2>/dev/null || echo '?')" || true
  command -v php &>/dev/null && echo "php: $(php -v 2>/dev/null | head -1 || echo '?')" || true
  command -v gcc &>/dev/null && echo "gcc: $(gcc --version 2>/dev/null | head -1 || echo '?')" || true
  command -v g++ &>/dev/null && echo "g++: $(g++ --version 2>/dev/null | head -1 || echo '?')" || true
  command -v mise &>/dev/null && echo "mise: $(mise -v 2>/dev/null || echo '?')" || echo "mise: (not installed)" || true
  exit 0
fi

# Install path: apt-get update once
apt-get update -y

# Core packages
apt-get install -y ca-certificates curl git gnupg \
  build-essential make pkg-config gcc g++ \
  python3 python3-venv python3-pip \
  golang-go \
  ruby-full \
  default-jdk-headless \
  rustc cargo \
  php-cli composer

# Node.js (only if missing)
if ! command -v node &>/dev/null; then
  NODESOURCE_OK=0
  if mkdir -p /etc/apt/keyrings 2>/dev/null; then
    if curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key 2>/dev/null | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg 2>/dev/null; then
      echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_20.x nodistro main" > /etc/apt/sources.list.d/nodesource.list
      apt-get update -y
      if apt-get install -y nodejs 2>/dev/null; then
        NODESOURCE_OK=1
      fi
    fi
  fi
  if [ "$NODESOURCE_OK" = "0" ]; then
    echo "NodeSource install failed, falling back to Ubuntu nodejs"
    apt-get install -y nodejs npm 2>/dev/null || apt-get install -y nodejs 2>/dev/null || true
    echo "Installed Ubuntu Node (older) — run devup vm provision again later for LTS"
  fi
fi

# Optional: cmake, clang
apt-get install -y cmake clang 2>/dev/null || true

# pnpm (only if npm exists)
if command -v npm &>/dev/null; then
  npm config set prefix /usr/local 2>/dev/null || true
  npm i -g pnpm@9 2>/dev/null || true
fi

# mise (JIT toolchain manager -- single binary, no deps)
if ! command -v mise &>/dev/null; then
  curl -fsSL https://mise.run 2>/dev/null | sh 2>/dev/null || true
  if [ -f "$HOME/.local/bin/mise" ]; then
    ln -sf "$HOME/.local/bin/mise" /usr/local/bin/mise
  fi
fi
mkdir -p /opt/devup/mise

# Verify core tools
MISSING=""
command -v node &>/dev/null || MISSING="$MISSING node"
command -v python3 &>/dev/null || MISSING="$MISSING python3"
command -v go &>/dev/null || MISSING="$MISSING go"
command -v ruby &>/dev/null || MISSING="$MISSING ruby"
command -v java &>/dev/null || MISSING="$MISSING java"
command -v cargo &>/dev/null || MISSING="$MISSING cargo"
command -v php &>/dev/null || MISSING="$MISSING php"
command -v gcc &>/dev/null || MISSING="$MISSING gcc"
if [ -n "$MISSING" ]; then
  echo "ERROR: Core toolchains missing after provisioning:$MISSING" >&2
  exit 1
fi

# Summary
echo "=== Toolchains installed ==="
command -v node &>/dev/null && echo "node: $(node -v 2>/dev/null || echo '?')" || true
command -v npm &>/dev/null && echo "npm: $(npm -v 2>/dev/null || echo '?')" || true
command -v python3 &>/dev/null && echo "python3: $(python3 -V 2>&1 || echo '?')" || true
command -v pip &>/dev/null && echo "pip: $(pip -V 2>/dev/null | head -1 || echo '?')" || command -v pip3 &>/dev/null && echo "pip: $(pip3 -V 2>/dev/null | head -1 || echo '?')" || true
command -v go &>/dev/null && echo "go: $(go version 2>/dev/null || echo '?')" || true
command -v ruby &>/dev/null && echo "ruby: $(ruby -v 2>/dev/null || echo '?')" || true
command -v java &>/dev/null && echo "java: $(java -version 2>&1 | head -1 || echo '?')" || true
command -v cargo &>/dev/null && echo "cargo: $(cargo -V 2>/dev/null || echo '?')" || true
command -v rustc &>/dev/null && echo "rustc: $(rustc -V 2>/dev/null || echo '?')" || true
command -v php &>/dev/null && echo "php: $(php -v 2>/dev/null | head -1 || echo '?')" || true
command -v composer &>/dev/null && echo "composer: $(composer --version 2>/dev/null || echo '?')" || echo "composer: (not installed)" || true
command -v gcc &>/dev/null && echo "gcc: $(gcc --version 2>/dev/null | head -1 || echo '?')" || true
command -v g++ &>/dev/null && echo "g++: $(g++ --version 2>/dev/null | head -1 || echo '?')" || true
command -v cmake &>/dev/null && echo "cmake: $(cmake --version 2>/dev/null | head -1 || echo '?')" || echo "cmake: (not installed)" || true
command -v pnpm &>/dev/null && echo "pnpm: $(pnpm -v 2>/dev/null || echo '?')" || echo "pnpm: (not installed)" || true
command -v mise &>/dev/null && echo "mise: $(mise -v 2>/dev/null || echo '?')" || echo "mise: (not installed)" || true
