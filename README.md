# Journalol

Journalol is a private, local-first League of Legends training journal. It is
designed around a simple loop: choose a focus, review games, and track progress
across a training block.

This repository is in its first implementation slice. Demo data is available,
but Riot account setup and Riot API match syncing are **not implemented yet**.
The optional AI coach is also deferred until the core journal is reliable.

## Start the demo

You need Docker with Docker Compose. Then run:

```sh
docker compose up --build
```

Open <http://127.0.0.1:8080>. Compose enables demo mode by default and stores
the SQLite database in a named Docker volume, so it survives container
restarts.

Stop the app with `Ctrl-C`, then run:

```sh
docker compose down
```

To use another browser port or timezone:

```sh
JOURNALOL_PORT=8081 JOURNALOL_TIMEZONE=America/Los_Angeles docker compose up --build
```

## Local development

Local development requires Go 1.26.

```sh
make demo
```

The local server listens at <http://127.0.0.1:8080> and writes
`journalol.db` in the repository directory.

Useful commands:

```sh
make test       # Run the test suite
make check      # Format, vet, and test
make build      # Build bin/journalol
make docker-up  # Build and run with Compose
```

Runtime configuration:

| Variable | Local default | Purpose |
| --- | --- | --- |
| `JOURNALOL_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `JOURNALOL_DB_PATH` | `./journalol.db` | SQLite database path |
| `JOURNALOL_TIMEZONE` | `UTC` | Timezone used when displaying dates |
| `JOURNALOL_DEMO` | `true` | Seed and use synthetic demo content |

Pass overrides to Make when needed, for example:

```sh
make demo JOURNALOL_TIMEZONE=America/Los_Angeles
```

Until Riot account setup is implemented, a fresh database needs demo mode (or
an earlier `journalol seed-demo` run). Starting with
`JOURNALOL_DEMO=false` and no primary profile exits with a clear error instead
of serving an unusable empty app.

## Current scope

The current milestone is an offline vertical slice: the app shell, local
storage, synthetic matches, training focus, reviews, and basic progress. Riot
API integration comes in a later milestone. Journalol is not intended to
replace general-purpose statistics sites such as OP.GG; external stats can be
linked when they are useful.
