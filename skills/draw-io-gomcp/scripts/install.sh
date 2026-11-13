#!/usr/bin/env bash
#
# draw-io-gomcp installer
# Detects platform, fetches the latest release binary (or builds from source),
# installs it, and registers it as an MCP server in the detected client.
#
set -euo pipefail

REPO="aosfather/draw-io-gomcp"
BIN_NAME="draw-io-gomcp"
INSTALL_DIR="${HOME}/.local/bin"
BIN="${INSTALL_DIR}/${BIN_NAME}"

echo "== draw-io-gomcp installer =="

# --- 1. detect OS / arch ---
OS="$(uname -s 2>/dev/null || echo unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"
case "$OS" in
  Darwin) GOOS=darwin ;;
  Linux)  GOOS=linux ;;
  MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
  *) echo "Unsupported OS: $OS. Please install manually (see skill README)." >&2; exit 1 ;;
esac
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  arm64|aarch64) GOARCH=arm64 ;;
  *) echo "Unsupported arch: $ARCH. Will try building from source."; GOARCH="" ;;
esac
echo "Detected platform: ${GOOS}/${GOARCH:-unknown}"

# --- 2. obtain the binary ---
get_binary() {
  if [ -n "${GOARCH}" ]; then
    echo "Looking up latest release of ${REPO}..."
    ASSETS=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -oE '"browser_download_url":[[:space:]]*"[^"]+"' \
      | sed -E 's/.*"browser_download_url":[[:space:]]*"([^"]+)"/\1/') || true
    DL=""
    while IFS= read -r u; do
      [ -z "$u" ] && continue
      if printf '%s' "$u" | grep -qiE "${GOOS}.*${GOARCH}|${GOARCH}.*${GOOS}"; then DL="$u"; break; fi
    done <<< "$ASSETS"
    if [ -n "$DL" ]; then
      echo "Downloading: $DL"
      mkdir -p "$INSTALL_DIR"
      TMP=$(mktemp -d)
      curl -fsSL "$DL" -o "${TMP}/asset"
      # unpack: support .tar.gz, .tgz, .zip, or raw binary
      case "$DL" in
        *.tar.gz|*.tgz) tar -xzf "${TMP}/asset" -C "$TMP" ;;
        *.zip) unzip -oq "${TMP}/asset" -d "$TMP" ;;
      esac
      # find the binary inside the extracted content (raw download has none)
      FOUND=$(find "$TMP" -type f -name "${BIN_NAME}*" 2>/dev/null | head -1)
      if [ -z "$FOUND" ]; then FOUND="${TMP}/asset"; fi
      install -m 0755 "$FOUND" "$BIN"
      rm -rf "$TMP"
      echo "Installed to $BIN"
      return 0
    fi
    echo "No prebuilt asset matched; will try building from source."
  fi

  if command -v go >/dev/null 2>&1; then
    echo "Building from source with 'go build'..."
    TMP=$(mktemp -d)
    git clone --depth 1 "https://github.com/${REPO}.git" "$TMP/src" >/dev/null 2>&1
    ( cd "$TMP/src" && go build -o "$BIN" ./cmd/server )
    rm -rf "$TMP"
    echo "Built and installed to $BIN"
    return 0
  fi

  echo "ERROR: could not download a prebuilt binary and 'go' is not installed." >&2
  echo "Either install Go (https://go.dev/dl/) or download a release manually:" >&2
  echo "  https://github.com/${REPO}/releases" >&2
  exit 1
}
get_binary

# --- 3. locate the MCP client config ---
# Priority: WorkBuddy -> Claude Desktop (macOS) -> Cursor -> fallback WorkBuddy path
CFG=""
if [ -f "${HOME}/.workbuddy/mcp.json" ]; then CFG="${HOME}/.workbuddy/mcp.json"; CLIENT="WorkBuddy";
elif [ -f "${HOME}/Library/Application Support/Claude/claude_desktop_config.json" ]; then CFG="${HOME}/Library/Application Support/Claude/claude_desktop_config.json"; CLIENT="Claude Desktop";
elif [ -f "${HOME}/.cursor/mcp.json" ]; then CFG="${HOME}/.cursor/mcp.json"; CLIENT="Cursor";
else CFG="${HOME}/.workbuddy/mcp.json"; CLIENT="WorkBuddy (new)"; fi
echo "Target MCP client: ${CLIENT} -> ${CFG}"

# --- 4. merge the server entry (idempotent) ---
python3 - "$CFG" "$BIN" <<'PY'
import sys, json, os
cfg, binpath = sys.argv[1], sys.argv[2]
obj = {}
if os.path.exists(cfg):
    try:
        with open(cfg, encoding="utf-8") as f:
            obj = json.load(f)
    except Exception:
        obj = {}
servers = obj.setdefault("mcpServers", {})
if "draw-io" in servers:
    print("draw-io entry already present; leaving it as-is.")
else:
    servers["draw-io"] = {"command": binpath, "args": [], "env": {}}
    os.makedirs(os.path.dirname(cfg), exist_ok=True)
    with open(cfg, "w", encoding="utf-8") as f:
        json.dump(obj, f, indent=2, ensure_ascii=False)
    print("Added draw-io server entry to config.")
PY

echo
echo "== done =="
echo "Config block written:"
echo '  "draw-io": { "command": "'"$BIN"'", "args": [], "env": {} }'
echo
echo "Next: trust/enable the 'draw-io' server in your client, then start a new session."
echo "The 14 draw.io tools (start_session, get_guide, create_new_diagram, edit_diagram, ...) will appear automatically."
