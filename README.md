# Journalol

Journalol is a private, local-first League of Legends training journal. It is
designed around a simple loop: choose a focus, review games, and track progress
across a training block.

Journalol can run with synthetic demo data or sync recent matches for one Riot
account. Its first coach integration is a local, read-only MCP server for the
ChatGPT desktop app: ChatGPT supplies the conversation while Journalol supplies
the player's private training context.

## Start the demo

You need Docker with Docker Compose. Then run:

```sh
docker compose up --build
```

Open <http://127.0.0.1:8080>. Compose enables demo mode by default and stores
the SQLite database in the local, gitignored `./data` directory, so it survives
container restarts and is straightforward to back up or move.

Stop the app with `Ctrl-C`, then run:

```sh
docker compose down
```

To use another browser port or timezone:

```sh
JOURNALOL_PORT=8081 JOURNALOL_TIMEZONE=America/Los_Angeles docker compose up --build
```

## Connect a Riot account

Riot's development key is enough for local use:

1. Sign in at the [Riot Developer Portal](https://developer.riotgames.com/).
   The dashboard automatically issues a development API key.
2. Copy the example configuration:

   ```sh
   cp .env.example .env
   ```

3. Edit `.env`: paste the key, split your public Riot ID (`GameName#Tag`) into
   the game-name and tag-line fields, and choose the platform where you play.
4. Start Journalol:

   ```sh
   docker compose up --build
   ```

Compose reads `.env` and forwards only the supported settings to the app.
`.env` is gitignored; do not commit an API key. A development key expires every
24 hours, so if syncing starts returning an authorization error, renew the key
on the developer dashboard, update `.env`, and rerun the same
`docker compose ... up -d` command (including `-p journalol-riot` when used) so
the container is recreated with the new value. For a longer-lived key for
private use, use **Register Product** in the portal and apply for a
**Personal Project**.

The platform route is the League server where the account plays; it is not
necessarily the same as the Riot ID tag. Common values include `NA1`, `EUW1`,
`EUN1`, `KR`, `JP1`, `BR1`, `LA1`, `LA2`, `OC1`, `TR1`, and `RU`. Journalol
derives the Match API's regional route from it. Leave
`JOURNALOL_RIOT_REGIONAL_ROUTE` blank unless you specifically need to override
that mapping.

Use `JOURNALOL_DEMO=false` when starting a fresh database for real Riot data.
If `./data` already contains the demo profile, preserve it and choose a
separate local data directory for the real journal:

```sh
JOURNALOL_DATA_DIR=./data-riot docker compose up --build
```

That creates `./data-riot`; it does not delete the demo journal in `./data`.
To switch back later, stop the real-data app, move the real-account environment
aside, and start with the default data directory:

```sh
docker compose down
mv .env .env.riot
docker compose up
```

Move `.env.riot` back to `.env` and set `JOURNALOL_DATA_DIR=./data-riot` before
starting the real-data journal again.

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

## Use Journalol with ChatGPT desktop

Journalol includes a **read-only local MCP server**. It does not need an OpenAI
API key: configure it in the ChatGPT desktop app, then use your existing
ChatGPT account for the coaching conversation. The MCP server can read match
summaries, completed player reviews, active training targets, and dashboard
progress. It cannot change notes, reviews, training blocks, or Riot settings.

For the portable bind-mounted Docker setup, build the host binary once and add
this local stdio server in ChatGPT desktop's MCP/connector settings. Replace
the project path and timezone with your values:

```json
{
  "command": "/absolute/path/to/journalol/bin/journalol",
  "args": ["mcp"],
  "env": {
    "JOURNALOL_DB_PATH": "/absolute/path/to/journalol/data/journalol.db",
    "JOURNALOL_DEMO": "false",
    "JOURNALOL_TIMEZONE": "America/Los_Angeles"
  }
}
```

Run `make build` after pulling changes that affect the connector. The host
binary and the container safely share the bind-mounted SQLite database; this
also works if you later stop using Docker. If you use a non-default Compose
directory, substitute that directory in `JOURNALOL_DB_PATH`.

Useful prompts once connected:

- “Give me a weekly retrospective from my Journalol data and a focused plan for the next five games.”
- “Review match 123 from Journalol. Separate the facts from your hypotheses and use my self-review.”
- “Look at my active training block and propose a lightweight daily check-in.”

Journalol does not expose the Riot API key, PUUID, raw Riot payloads, SQLite
access, or write actions through MCP. Treat coach recommendations as proposals;
edit plans and journal entries in Journalol yourself.

## Generate death clips from a downloaded replay

Journalol's host CLI can render one WebM review clip per imported player death.
It uses the death clock from Match-V5, validates that the `.rofl` filename
matches the selected Journalol game, and writes clips under `data/clips`.
Journalol does not download replays: first use **Download Replay** in the League
client. On macOS, Journalol then launches the downloaded replay itself; do not
open it first.

For the least disruptive capture, create a separate macOS desktop once, start
any replay there, Control-click the running **League game** Dock icon, and choose
**Options → Assign To → This Desktop**. Journalol temporarily runs that game in
a 1280×720 window. The assignment is a one-time macOS setting, not something
Journalol scripts.

Build the host binary, then run the command. The match number is the Journalol
ID shown in a match URL such as `/matches/27`:

```sh
make build
JOURNALOL_DB_PATH="$PWD/data/journalol.db" \
JOURNALOL_DEMO=false \
./bin/journalol capture death-clips \
  --match 27
```

When `--replay` is omitted, Journalol looks for the matching file in
`~/Documents/League of Legends/Replays`. Pass `--replay /path/to/game.rofl` for
a different directory. It reads the installed client's region and locale,
enables Riot's
[documented local Replay API](https://developer.riotgames.com/docs/lol#game-client-api_replay-api),
pre-seeks each time range, derives the primary player's spectator slot from the
imported participant ID, and uses League's native spectator control to keep the
top-down camera following that champion. Starting a Replay API recording
reconstructs replay state once more, so Journalol reapplies the follow action
after recording reports active instead of trusting the initial camera center.
Journalol verifies the launched process ID before sending the spectator key and
closes only that owned game process afterward; it does not activate League or
move the active macOS Space.

The focused camera needs macOS permission to send its replay camera keys
directly to the League game process. On the first run, allow the prompt or
enable the app that started Journalol under **System Settings → Privacy &
Security → Accessibility**. That is usually Terminal for a command you run
yourself, or Codex when capture is launched from Codex. Journalol checks this
before opening League and fails instead of silently recording Directed Camera
when permission is absent.

Each clip covers 60 seconds before the death and 10 seconds after it by
default. Recording is approximately real-time, so a default clip takes at
least about 70 seconds to render. Use `--before`, `--after`, `--fps`,
`--window-width`, and `--window-height` to adjust capture settings. Use
`--manual` only when you intentionally opened and configured a replay yourself
or are running outside macOS.

Before launch, Journalol saves an exact recovery copy of `game.cfg` under
`data/capture`; it temporarily disables Directed Camera in addition to the
window and Replay API settings. After the owned game exits, it restores the
original bytes and permissions. Ctrl-C performs the same cleanup, and an
encoder that stops making file progress is terminated rather than left open
indefinitely. If the host process is forcibly killed or the computer loses
power, close League and run:

```sh
JOURNALOL_DB_PATH="$PWD/data/journalol.db" \
JOURNALOL_DEMO=false \
./bin/journalol capture restore-config
```

The CLI records clip state in SQLite and stops on the first failed render,
preserving the error for later display. It uses Riot's loopback-only Replay API
for seeking and recording; it does not upload replay or video data.

Runtime configuration:

| Variable | Local default | Purpose |
| --- | --- | --- |
| `JOURNALOL_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `JOURNALOL_DB_PATH` | `./journalol.db` | SQLite database path |
| `JOURNALOL_DATA_DIR` | `./data` (Compose only) | Host directory bind-mounted to `/data` by Docker Compose |
| `JOURNALOL_TIMEZONE` | `UTC` | Timezone used when displaying dates |
| `JOURNALOL_DEMO` | `true` | Seed and use synthetic demo content |
| `RIOT_API_KEY` | unset | Runtime Riot API credential; Journalol never persists it |
| `RIOT_API_KEY_FILE` | unset | Alternative local file containing the Riot API key |
| `JOURNALOL_RIOT_GAME_NAME` | unset | Game-name portion of `GameName#Tag` |
| `JOURNALOL_RIOT_TAG_LINE` | unset | Tag-line portion of `GameName#Tag` |
| `JOURNALOL_RIOT_PLATFORM_ROUTE` | unset | League platform route, such as `NA1` |
| `JOURNALOL_RIOT_REGIONAL_ROUTE` | derived | Optional `AMERICAS`, `ASIA`, `EUROPE`, or `SEA` override |
| `JOURNALOL_RIOT_HISTORY_LIMIT` | `20` | Initial history and overlap page size per included queue (1–100) |
| `JOURNALOL_RIOT_POLL_INTERVAL` | `5m` | Background sync interval; `0` disables polling |
| `JOURNALOL_RIOT_SYNC_ON_START` | `true` | Sync once when the app starts |

Pass overrides to Make when needed, for example:

```sh
make demo JOURNALOL_TIMEZONE=America/Los_Angeles
```

`RIOT_API_KEY` takes precedence if both key settings are present.
`RIOT_API_KEY_FILE` is convenient when running the local binary. The stock
Compose setup does not mount host files, so use `RIOT_API_KEY` there unless you
add your own read-only secret mount. The key is not written to SQLite or sent
to browser code, but an environment-provided value can be visible through
Docker's container configuration to users who can inspect the Docker daemon.

## Current scope

The current milestone includes the app shell, local storage, synthetic demo
matches, Riot match import, training focus, reviews, and basic progress.
Riot sync is deliberately limited to Normal Draft, Ranked Solo/Duo, and Ranked
Flex. The match archive defaults to those three queues and can be narrowed to
Both, Ranked, Normal Draft, Solo/Duo, or Flex.
The local MCP coach connector exposes read-only aggregate, match, review, and
active-training context to ChatGPT desktop; it does not make in-app model calls
or require a model API key.
Journalol is not intended to replace general-purpose statistics sites such as
OP.GG; external stats can be linked when they are useful.
