#!/bin/sh
# Renders a daemon service file for the current OS with the given binary
# path substituted in. Writes the rendered file under dist/service/ and
# prints the manual command to install + enable it.
#
# This script never copies into a system service directory and never
# loads/enables a service itself — that step is left to the operator.
#
# Usage: scripts/install-service.sh <path-to-repomap-binary>

set -eu

BINARY_PATH="${1:?usage: $0 <path-to-repomap-binary>}"
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
OUT_DIR="$ROOT_DIR/dist/service"
LOG_DIR="${REPOMAP_LOG_DIR:-$HOME/.local/state/repomap}"

mkdir -p "$OUT_DIR"

render() {
	template="$1"
	out="$2"
	sed -e "s#__BINARY_PATH__#$BINARY_PATH#g" \
	    -e "s#__LOG_DIR__#$LOG_DIR#g" \
	    "$template" >"$out"
}

os="$(uname -s)"
case "$os" in
Darwin)
	template="$ROOT_DIR/contrib/launchd/com.repomap.daemon.plist.tmpl"
	out="$OUT_DIR/com.repomap.daemon.plist"
	render "$template" "$out"
	echo "Rendered launchd plist: $out"
	echo
	echo "To install (does not run automatically):"
	echo "  mkdir -p $LOG_DIR"
	echo "  cp $out ~/Library/LaunchAgents/com.repomap.daemon.plist"
	echo "  launchctl load ~/Library/LaunchAgents/com.repomap.daemon.plist"
	;;
Linux)
	template="$ROOT_DIR/contrib/systemd/repomap.service.tmpl"
	out="$OUT_DIR/repomap.service"
	render "$template" "$out"
	echo "Rendered systemd user unit: $out"
	echo
	echo "To install (does not run automatically):"
	echo "  mkdir -p $LOG_DIR ~/.config/systemd/user"
	echo "  cp $out ~/.config/systemd/user/repomap.service"
	echo "  systemctl --user enable --now repomap.service"
	;;
*)
	echo "No service template for OS '$os'." >&2
	echo "Templates are available under contrib/launchd and contrib/systemd" >&2
	echo "for reference; adapt one for your init system." >&2
	exit 1
	;;
esac
