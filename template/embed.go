package goagents_template

import "embed"

//go:embed PROMPT.ts MEMORY.md core/** tools/**
var FS embed.FS
