Build a small local web app for deliberate League of Legends training.

The app should sync match data from Riot’s API, store it locally, and combine automated stats with short manual post-game reflections. It is not meant to replace OP.GG; it should help a player improve through focused training blocks.

## Core features

### Player setup

* Configure Riot ID, region, and Riot API key.
* Fetch recent matches and automatically poll for new completed matches.
* Support one primary player profile initially.

### Match import

For each match, store:

* Champion, role, queue type, duration, result.
* K/D/A, CS, gold, damage, vision score.
* Wards placed, wards killed, control wards purchased.
* Items, runes, summoner spells, skill order.
* Objective participation where available.
* Raw match and timeline JSON for future analysis.

Avoid claiming metrics the Riot API cannot reliably provide.

### Training focus

Allow the user to define an active training block, such as:

* Die fewer than five times.
* Arrive before dragon fights.
* Improve vision score.
* Avoid facechecking.
* Track the enemy jungler.

Each focus should have:

* Name and description.
* Start/end date.
* One or more measurable targets.
* Status: planned, active, completed, abandoned.
* Notes and retrospective.

Show the active focus prominently before and after games.

### Post-game review

After importing a new match, prompt the user for a fast review:

* Grade the current focus: A–F or 1–5.
* Biggest mistake.
* One thing done well.
* One thing to do differently next game.
* Tag deaths or mistakes using categories such as:

  * Greed
  * Positioning
  * No vision
  * Facecheck
  * Mechanical error
  * Matchup knowledge
  * Jungle tracking
  * Bad engage
  * Late to objective

Keep this workflow under one minute.

### Dashboard

Show:

* Current training focus.
* Recent matches.
* Win rate, deaths/game, KDA, vision score/minute, control wards/game.
* Trend lines over the last 10, 20, and 50 games.
* Performance by champion.
* Most common mistake categories.
* Progress against the active focus.
* Small summary such as: “Deaths improved from 7.1 to 5.4 over the last 20 games.”

Do not overemphasize win rate; focus on controllable metrics.

### Champion pool

Track:

* Main champions.
* Archetype: enchanter, hook, melee engage, mage, jungle, etc.
* Games played.
* Confidence level.
* Personal notes.
* Common matchups.
* Preferred build and rune references.
* Current learning priority.

### Session planning

Before queueing, show a short session card:

* Current focus.
* One reminder.
* Champion pool.
* Optional warm-up checklist.
* Number of planned games.

After the session, show a summary across those games.

### Review and search

* Filter matches by champion, role, queue, result, date, and training focus.
* Search manual notes.
* Open a match detail page with imported stats, timeline events, and reflections.
* Export data as JSON or CSV.
* Back up and restore the local database.

## Product constraints

* Local-first.
* SQLite database.
* Single-user.
* Runs with Docker Compose.
* Clean, minimal UI.
* Desktop-first but usable on mobile.
* No cloud account required.
* Keep Riot API access behind a small backend service.
* Preserve raw API responses so analysis can improve later.
* Handle API rate limits, expired keys, missing timeline data, remakes, and duplicate imports.
* Include seed/demo data so the UI can be tested without a Riot API key.

## Suggested stack

Use a simple stack with:

* Backend: Go
* Frontend: lightweight server-rendered HTML or a small React app.
* Database: SQLite.
* Charts: a minimal charting library.
* Background job for match polling.

Generate:

* Architecture overview.
* Database schema.
* API endpoints.
* Docker Compose setup.
* Initial working implementation.
* README with local setup and Riot developer-key configuration.
* Tests for match import, deduplication, metric calculation, and training-focus progress.
