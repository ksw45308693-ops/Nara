// Package web contains the server-rendered templates and static assets.
package web

import "embed"

// Files is the application UI bundled into the Go binary.
//
//go:embed templates/*.html static/*
var Files embed.FS
