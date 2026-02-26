package goagents_template

import "embed"

//go:embed PROMPT.ts PROMPT_HARNESS_API.ts MEMORY.md core/** tools/**
var FS embed.FS
