# Dev container for jkit. Mirrors CI: Go 1.25 + golangci-lint pinned to the same
# version (.github/workflows/ci.yml). Keep these versions in sync.
FROM golang:1.25

# golangci-lint, version-pinned. Copying the binary from the official image
# avoids running an install script and keeps the version explicit.
COPY --from=golangci/golangci-lint:v2.12.2 /usr/bin/golangci-lint /usr/local/bin/golangci-lint

WORKDIR /workspaces/jkit
