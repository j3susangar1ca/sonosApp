#!/bin/bash
# ============================================
# Script de Configuración - Office Jukebox
# ============================================

set -e

echo "🎵 Office Jukebox - Script de Configuración"
echo "==========================================="

# Colores para output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Función para verificar dependencias
check_dependency() {
    if command -v "$1" &> /dev/null; then
        echo -e "${GREEN}✓${NC} $1 instalado: $($1 --version 2>&1 | head -1)"
        return 0
    else
        echo -e "${RED}✗${NC} $1 NO encontrado"
        return 1
    fi
}

# Función para cargar variables de entorno
load_env() {
    if [ -f ".env" ]; then
        echo -e "${GREEN}✓${NC} Cargando variables desde .env"
        export $(grep -v '^#' .env | xargs)
    else
        echo -e "${YELLOW}!${NC} No se encontró .env, usando valores por defecto"
    fi
}

# Verificar directorio
cd "$(dirname "$0")"

echo ""
echo "1. Verificando dependencias..."
echo "------------------------------"
check_dependency "go" || true
check_dependency "yt-dlp" || true
check_dependency "node" || true

echo ""
echo "2. Cargando variables de entorno..."
echo "------------------------------------"
load_env

echo ""
echo "3. Verificando binario del servidor..."
echo "---------------------------------------"
if [ -f "./jukebox-server" ]; then
    echo -e "${GREEN}✓${NC} jukebox-server encontrado"
    ./jukebox-server --help 2>&1 | head -5
else
    echo -e "${YELLOW}!${NC} jukebox-server no encontrado, construyendo..."
    go build -o jukebox-server ./cmd/jukebox-server
fi

echo ""
echo "4. Estado de archivos de persistencia..."
echo "-----------------------------------------"
if [ -f "state.json" ]; then
    echo -e "${GREEN}✓${NC} state.json existe"
    cat state.json
else
    echo -e "${YELLOW}!${NC} state.json no existe (se creará al iniciar)"
fi

echo ""
echo "==========================================="
echo -e "${GREEN}✓${NC} Configuración completada"
echo ""
echo "Para iniciar el servidor en modo mock (sin Sonos):"
echo "  ./jukebox-server --port 8080"
echo ""
echo "Para iniciar con un dispositivo Sonos:"
echo "  ./jukebox-server --sonos-ip <IP_DEL_SONOS>"
echo ""
echo "Para múltiples zonas:"
echo "  ./jukebox-server --zone default:<IP1> --zone kitchen:<IP2>"
echo ""
