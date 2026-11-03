//go:build tools
// +build tools

// Package tools pins build/runtime dependencies so that `go mod tidy` keeps
// them in go.mod and go.sum even though the main product does not import them
// yet (their real usage lands in later Foundation tasks). This is the minimal
// pinning choice for the F00 toolchain freeze: blank-import without invoking.
package tools

import (
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/wailsapp/wails/v3/pkg/application"
	_ "modernc.org/sqlite"
)
