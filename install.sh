#!/bin/sh
set -eu

REPO="mindsdb/yolocoder"
INSTALL_DIR="${YOLOCODER_INSTALL_DIR:-$HOME/.local/bin}"
BIN_NAME="yolocoder"

die() { printf 'yolocoder: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required but was not found on PATH"; }

detect_platform() {
	PLATFORM_OS="$(uname -s)"
	case "$PLATFORM_OS" in Darwin | Linux) ;; *) die "unsupported OS: $PLATFORM_OS" ;; esac
	case "$(uname -m)" in
	arm64 | aarch64) PLATFORM_ARCH="arm64" ;;
	x86_64 | amd64) PLATFORM_ARCH="x86_64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
	else die "sha256sum or shasum is required"; fi
}

main() {
	need curl
	need tar
	detect_platform
	version="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
	version="${version##*/}"
	[ -n "$version" ] || die "could not determine the latest release"
	asset="yolocoder_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
	base_url="https://github.com/$REPO/releases/download/$version"
	workdir="$(mktemp -d)"
	trap 'rm -rf "$workdir"' EXIT
	printf 'Installing YoloCoder %s for %s/%s...\n' "$version" "$PLATFORM_OS" "$PLATFORM_ARCH"
	curl -fsSL -o "$workdir/$asset" "$base_url/$asset" || die "could not download $asset"
	curl -fsSL -o "$workdir/checksums.txt" "$base_url/checksums.txt" || die "could not download checksums"
	expected="$(awk -v file="$asset" '$2 == file {print $1}' "$workdir/checksums.txt")"
	[ -n "$expected" ] || die "$asset is missing from checksums.txt"
	[ "$expected" = "$(sha256_of "$workdir/$asset")" ] || die "checksum mismatch for $asset"
	tar -xzf "$workdir/$asset" -C "$workdir"
	[ -f "$workdir/$BIN_NAME" ] || die "$asset did not contain $BIN_NAME"
	mkdir -p "$INSTALL_DIR"
	install -m 755 "$workdir/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME" 2>/dev/null || {
		cp "$workdir/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
		chmod 755 "$INSTALL_DIR/$BIN_NAME"
	}
	printf 'Installed %s to %s/%s\n' "$BIN_NAME" "$INSTALL_DIR" "$BIN_NAME"
}

main "$@"
