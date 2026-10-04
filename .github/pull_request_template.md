## What

## Why

## Checklist

- [ ] `CGO_ENABLED=0 go test ./...` passes
- [ ] New or changed adapter ships a fixture under `testdata/<agent>/` with a fail → edit → pass sequence, one dropped event, and a planted secret
- [ ] Fixtures contain no real usernames, home paths, hostnames, tokens, or private output
