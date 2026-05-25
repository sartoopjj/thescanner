//go:build tools
// +build tools

// Package tools pins build-time-only dependencies so `go mod tidy`
// doesn't drop them.
//
// gomobile bind requires `golang.org/x/mobile` in the module
// dependency graph (not just on PATH). Nothing in regular Go code
// imports it — the mobile/ package is consumed by gomobile itself,
// not by Go imports — so without a pin like this, every `go mod tidy`
// removes it and the next `gomobile bind` fails with:
//
//   gomobile bind requires golang.org/x/mobile in the current module,
//   but it is not in the module dependency graph.
//
// The `//go:build tools` constraint makes this file invisible to all
// normal builds (it never compiles into any output), but `go mod
// tidy` still scans its imports — exactly what we want.
//
// Go 1.24+ has a native `tool` directive in go.mod that does the same
// job more cleanly. When this module bumps to 1.24, delete this file
// and run:
//   go get -tool golang.org/x/mobile/cmd/gobind
package tools

import (
	_ "golang.org/x/mobile/bind"
	_ "golang.org/x/mobile/cmd/gobind"
	_ "golang.org/x/mobile/cmd/gomobile"
)
