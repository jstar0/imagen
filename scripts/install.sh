#!/usr/bin/env bash
set -euo pipefail
SOURCE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNTIME_OUT="${RUNTIME_OUT:-$HOME/.local/share/imagen/bin}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
CONFIG_DIR="${CONFIG_DIR:-$HOME/.config/imagen}"
BACKUP_DIR="${BACKUP_DIR:-$HOME/.local/state/imagen/install-backups/$(date +%Y%m%dT%H%M%S)}"
if [[ -n "${IMAGEN_CONFIG_SOURCE:-}" ]]; then
  [[ -f "$IMAGEN_CONFIG_SOURCE" ]] || { echo 'IMAGEN_CONFIG_SOURCE is not a file' >&2; exit 1; }
fi
mkdir -p "$RUNTIME_OUT" "$BIN_DIR" "$CONFIG_DIR" "$BACKUP_DIR"
index=0
for path in "$RUNTIME_OUT/imagen" "$BIN_DIR/imagen" "$CONFIG_DIR/config.json"; do
  index=$((index + 1))
  if [[ -e "$path" || -L "$path" ]]; then
    parent="$(basename "$(dirname "$path")")"
    cp -pP "$path" "$BACKUP_DIR/$index-$parent-$(basename "$path")"
  fi
done
temporary="$(mktemp "$RUNTIME_OUT/.imagen-XXXXXX")"
trap 'rm -f "$temporary"' EXIT
cd "$SOURCE"
revision="$(git rev-parse --short HEAD 2>/dev/null || true)"
if [[ -n "$revision" ]] && [[ -n "$(git status --porcelain)" ]]; then revision="$revision-dirty"; fi
go build -trimpath -ldflags="-s -w -X github.com/jstar0/imagen/internal/imagen.BuildCommit=$revision" -o "$temporary" ./cmd/imagen
chmod 755 "$temporary"
mv -f "$temporary" "$RUNTIME_OUT/imagen"
ln -sfn "$RUNTIME_OUT/imagen" "$BIN_DIR/imagen"
if [[ -n "${IMAGEN_CONFIG_SOURCE:-}" ]]; then
  [[ -f "$IMAGEN_CONFIG_SOURCE" ]] || { echo 'IMAGEN_CONFIG_SOURCE is not a file' >&2; exit 1; }
  ln -sfn "$IMAGEN_CONFIG_SOURCE" "$CONFIG_DIR/config.json"
fi
printf 'binary=%s\nsource=%s\nrevision=%s\nbackup=%s\n' "$BIN_DIR/imagen" "$SOURCE" "$revision" "$BACKUP_DIR"
