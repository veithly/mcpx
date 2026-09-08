#!/bin/bash
# Run with: MCPX_SIGN_IDENTITY='Apple Development: ...' bash scripts/sign-macos-stable.sh bin/mcpx-server
# Uses an existing certificate. Never creates trust roots, resets TCC or modifies a running installation.
set -euo pipefail
[[ $(uname -s) == Darwin ]] || { echo 'Stable macOS signing must run on macOS.' >&2; exit 2; }
[[ $# == 1 && -f "$1" && ! -L "$1" ]] || { echo 'Usage: sign-macos-stable.sh <regular Mach-O binary>' >&2; exit 2; }
identity=${MCPX_SIGN_IDENTITY:-}
[[ -n "$identity" && "$identity" != '-' ]] || { echo 'MCPX_SIGN_IDENTITY must name an existing code-signing certificate, not an ad-hoc identity. No file was changed.' >&2; exit 2; }
file "$1" | grep -q 'Mach-O' || { echo 'Input must be a Mach-O executable.' >&2; exit 2; }
# Identifier and signing certificate must remain consistent across versions.
/usr/bin/codesign --force --sign "$identity" --identifier com.mcpx.server --options runtime "$1"
/usr/bin/codesign --verify --strict --verbose=2 "$1"
/usr/bin/codesign --display --requirements - --verbose=2 "$1"
echo 'Signed candidate only. Keep the same certificate and installed path across updates. Existing ad-hoc grants may require one migration approval.'
