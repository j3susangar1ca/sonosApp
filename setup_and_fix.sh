#!/bin/bash
# ================================================
# Instalación y parcheo de sonosApp
# ================================================
# Este script:
# 1. Instala Deno (runtime JS para yt-dlp)
# 2. Configura yt-dlp con componentes EJS remotos
# 3. Copia los archivos corregidos al repositorio
# 4. Compila el servidor
#
# USO:
#   chmod +x setup_and_fix.sh
#   ./setup_and_fix.sh /ruta/al/repositorio/sonosApp
# ================================================

set -e

SONOS_APP_DIR="${1:-.}"

echo "=== sonosApp Fix Setup ==="
echo "Target directory: $SONOS_APP_DIR"

# --- 1. Install Deno ---
echo ""
echo "[1/4] Installing Deno..."
if command -v deno &>/dev/null; then
    echo "  Deno already installed: $(deno --version | head -1)"
else
    curl -fsSL https://deno.land/install.sh | sh
    echo '  Add to your ~/.bashrc or ~/.zshrc:'
    echo '  export PATH="$HOME/.deno/bin:$PATH"'
    export PATH="$HOME/.deno/bin:$PATH"
    echo "  Deno installed: $(deno --version | head -1)"
fi

# --- 2. Configure yt-dlp EJS ---
echo ""
echo "[2/4] Configuring yt-dlp with remote EJS components..."
mkdir -p ~/.config/yt-dlp/
echo "--remote-components ejs:github" > ~/.config/yt-dlp/config
echo "  Written: ~/.config/yt-dlp/config"
echo "  Contents: $(cat ~/.config/yt-dlp/config)"

# --- 4. Build ---
echo ""
echo "[4/4] Building server..."
cd "$SONOS_APP_DIR"

if command -v go &>/dev/null; then
    go build -o jukebox-server-fixed ./cmd/jukebox-server/
    echo "  ✓ Build successful: jukebox-server-fixed"
else
    echo "  ⚠ Go not found in PATH. Install Go 1.24+ and run:"
    echo "    go build -o jukebox-server-fixed ./cmd/jukebox-server/"
fi

echo ""
echo "=== Setup Complete! ==="
echo ""
echo "To run the server:"
echo "  export PATH=\"\$HOME/.deno/bin:\$PATH\""
echo "  ./jukebox-server-fixed \\"
echo "    -port 8080 \\"
echo "    -sonos-ip 192.168.1.64 \\"
echo "    -server-url http://192.168.1.67:8080"
echo ""
echo "IMPORTANT: -server-url must be the URL that Sonos uses to reach THIS server."
echo "           Use your machine's LAN IP (e.g. 192.168.1.67), NOT localhost."
