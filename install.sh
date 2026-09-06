#!/bin/sh
set -e

INSTALL_DIR="$HOME/.local/bin"
BINARY="ce"

echo "Building $BINARY..."
go build -o "$BINARY" ./

mkdir -p "$INSTALL_DIR"
mv "$BINARY" "$INSTALL_DIR/$BINARY"
echo "Installed to $INSTALL_DIR/$BINARY"

# Warn if the install dir is not in PATH.
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "Warning: $INSTALL_DIR is not in your PATH" ;;
esac
