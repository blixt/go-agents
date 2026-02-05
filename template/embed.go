package karna_template

import "embed"

//go:embed PROMPT.ts core/** tools/**
var FS embed.FS
