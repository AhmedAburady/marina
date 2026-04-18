# Git commands
mod git


# Install Dev
install:
  go install -ldflags "-X main.version=dev" ./cmd/marina

# Build binary
build:
  go build -ldflags "-X main.version=dev" -o marina ./cmd/marina 2>&1