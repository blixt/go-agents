# Agent Instructions (`go-agents`)

## Tooling Policy

Use `mise` for all routine project commands so tool versions and env are consistent.

- `test`: `mise run test`
- `format` (fmt + vet + lint): `mise run format`
- `start`: `mise run start`
- `dev`: `mise run dev`

Do not run these directly:

- `go test ./...`
- `go vet ./...`
- `golangci-lint run`
- `docker compose ...` for normal dev/start flows

## If A Task Is Missing

If a needed task is not defined in `mise.toml`, prefer:

- `mise exec -- <command>`

Only add ad-hoc direct commands when there is no practical `mise` route.
