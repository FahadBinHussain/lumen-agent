# Element Orion fork (FahadBinHussain/lumen-agent) — agent notes

## Merged platforms (2026-08-12): murmur Messenger + WhatsApp ports

This fork now runs all three platforms from one binary: the upstream Element Orion
Discord agent runtime, plus Messenger and WhatsApp channels ported from the murmur
repo (`C:\Users\Admin\Downloads\murmur`). **The murmur repo stays untouched** —
it is a read-only source for this port (its HF Space keeps running during cutover).

Port layout:
- `internal/messenger/` — messagix (Meta MQTT) client: cookies load, login, event
  handler (incoming messages + 60s foreground keepalive), send (chunked @1900),
  edit/delete, MercuryUpload image send, cookie reload. Trigger/mention helpers
  (`MentionsMe`, `CleanMentions`) live here because they need the logged-in uid.
- `internal/whatsapp/` — embedded whatsmeow (no wacli binary): sqlite device store,
  Chrome uTLS fingerprint HTTP client (bypasses JA3), `JIDToThreadID`, parsed types.
- `internal/neon/` — Neon `whatsapp_sessions` persistence for the whatsmeow db file
  (saved on connect + every 15 min + on shutdown).
- `internal/cookies/` — verbatim port of murmur's cookie loader/convert.
- `internal/bridge/` — glue service: sessions per `platform:thread` fed into the
  agent runner (`agent.Runner.Run`), plus the HTTP server.

Config (lumen.yaml / element-orion.example.yaml): top-level `messenger:`, `whatsapp:`,
`bridge:` sections. `bridge.listen_addr` default `127.0.0.1:8791`.

Bridge HTTP endpoints:
- `POST <bridge.listen_addr>/api/automation/notifications` — port of murmur's
  endpoint used by the Vercel pollers (`triton.vercel.app/api/steam-updates` and
  `/api/free-games`). Body `{source, threadId, title, message, dedupeKey, url,
  platform}`; `platform` defaults to `messenger` (backward compatible), `whatsapp`
  takes the JID in `threadId`. Optional auth: `bridge.secret` /
  `ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET` env; header `X-HF-Authorization`
  (pollers already send it) or `Authorization: Bearer`.
- `POST /api/cookies/upload` — murmur-cookie-refresher.mjs contract (only mounted
  when messenger.enabled). Writes cookies file + reloads the messagix client.
  Auth: same secret handling as notifications (X-HF-Authorization or
  Authorization: Bearer; no-op when bridge.secret unset). NOT enabled yet on
  this box — messenger.enabled stays false until user picks the messenger
  account (they plan a different FB account than the murmur one).
- `GET /api/health`.

Trigger semantics (ported from murmur): Messenger replies when the message starts
with `/ai`, is a reply to one of our messages, or mentions our uid (mentions are
stripped from the prompt). WhatsApp replies only on `/ai` prefix. The `/ai` prefix
is stripped; the rest becomes the prompt for the agent runner. No murmur `/ai`
subcommands (models/image) — the fork uses the single `llm.model` config.

History: per `platform:thread` in memory, capped at 100 messages; the agent runner
trims per request.

## Platform gotchas

- `exec_command` tool is POSIX-only (upstream `exec.Command(shell, "-lc", ...)`) —
  keep it disabled/`pwsh`-only on Windows; the merge plan documents this constraint.
- Superseded murmur bits (better-native replacements, added 2026-08-12):
  - whatsapp typing indicators: whatsmeow `SendChatPresence` native
    (`ChatPresenceComposing`/`ChatPresencePaused`) via `StartTyping`/`StopTyping`
    on the client, wired in `bridge.agentRun` around the agent call. replaces
    murmur's wacli `sender.go` typing/presence.
  - image generation: `generate_image` tool (internal/tools/image_gen.go) hits
    `<llm.base_url>/images/generations` with `llm.api_key`; config section
    `image_gen:` (`enabled`, `model`, `output_dir`, default output
    `.element-orion/generated`). replaces murmur's `/ai image` subcommand —
    usable by the agent on all platforms, not just messenger. enables via
    `image_gen.enabled: true` + `generate_image` in `tools.enabled`.
  - dailyBNP outbox pull worker: `internal/bnp` is a faithful port of murmur's
    claim/ack worker (poll `BNP_MESSENGER_OUTBOX_URL` GET ?limit=N → send or
    edit via messagix → POST ack back). env contract identical to murmur
    (`BNP_MESSENGER_OUTBOX_URL/TOKEN/THREAD_ID`, `BNP_MESSENGER_POLL_SECONDS`,
    `BNP_MESSENGER_CLAIM_LIMIT`, `BNP_MESSENGER_REQUEST_TIMEOUT_SECONDS`).
    wired in `bridge.Service.Run`, starts only when `messenger.enabled` is true
    AND the env vars are set (worker logs "disabled" otherwise). sender
    interface (`MessengerSender`) defined in `internal/bnp` to avoid an import
    cycle; `Service.SendMessage`/`EditMessage` implement it. the push endpoint
    `POST /api/automation/notifications` (with `bridge.secret` auth) remains
    the alternative for sources that can push.
  - notifier kill-switches (added 2026-08-14): `bridge.notifications_enabled`
    and `bridge.bnp_enabled` (both default true in code; set FALSE in
    config/lumen.yaml). notifications_enabled=false unmounts ONLY the
    `/api/automation/notifications` handler — `/api/cookies/upload` is NOT
    gated (it's ops-critical for the browserless cookie refresher and stays
    mounted whenever messenger.enabled is true). bnp_enabled=false skips the
    BNP worker goroutine. both are currently FALSE so lumen stays quiet while
    murmur's live notifiers (steam-updates/free-games cron jobs, neon usage,
    BNP worker) keep running — re-enable per-notifier only when lumen
    actually takes over that flow.
  - notify pollers COPY (added 2026-08-14): `internal/notify` is a copy-only
    port of the three murmur-side feeds so lumen can become self-contained in
    an HF space: `steam_updates` (Steam GetNewsForApp Community Announcements,
    dedupe `steam_seen`), `free_games` (lootscraper Atom feed, amazon + blocked
    hosts skipped, dedupe `game_seen`), `neon_usage` (Neon org consumption via
    REST, warning at N CU-h per org/period with state-file dedupe). config
    section `notify:`; ALL pollers default disabled, lumen.yaml keeps them
    off. webhook = the bridge notifications endpoint (default murmur space
    URL; `notify.webhook_url` overrides). steam/free-games need
    `notify.database_url` (or DATABASE_URL env) for the dedupe tables;
    neon_usage needs `notify.neon_usage.api_key_env` (env var names holding
    Neon API keys) + `thread_id` + optional `state_path`. NOT wired yet on
    this box — copy only until cutover (mirrors bnp_enabled approach).
  - model-catalog listing (/ai models etc.) intentionally dropped — the fork
    uses the single `llm.model` config.
- Pre-existing test failures on this machine (NOT caused by the merge, verified):
  - `internal/discordbot` heartbeat test hardcodes Linux path `/workspace/lumen/...`
  - `internal/skills` test expects `.claude` workspace skills fixtures
  - `internal/tools` exec test needs `/bin/zsh`
  - Passing packages: config, eventwebhook, bridge, dashboard, heartbeatstate,
    httpaux, llm, sandbox, secrets.
- Messagix needs a full cookie set (`cookies.GetMissing` fails startup otherwise);
  refresh via `/api/cookies/upload` (murmur-cookie-refresher.mjs) without restart.
  Cookie source on this machine: agent-browser lightweight cookie vault
  (`%APPDATA%\mainframe\accounts\agent-browser\cookies\<email>.cookies.json`,
  saved with `agent-browser-account.ps1 cookies save <email>`), converted to the
  plain `{name:value}` map. The refresher is browserless since 2026-08-12 —
  point it at the local bridge with `MURMUR_HF_SPACE_URL=http://127.0.0.1:8791`
  and pick the account via `AGENT_BROWSER_EMAIL`.
- whatsmeow: first run has no device — QR is logged (`events.QR`) for phone scan;
  after linking, the device store file (`whatsmeow.db` under `whatsapp.store_dir`)
  is backed up to Neon so re-pairs survive machine moves.
- Old EO instance on this machine (PID 10772, since 2026-08-11) ran the
  pre-merge binary; replaced 2026-08-12 by the merged build (serve with
  config/lumen.yaml, bridge on 127.0.0.1:8791). messenger + whatsapp still
  disabled until user provisions the channels.

## DM speaker labels (2026-08-14 fork patch)

Upstream labels the speaker only in shared guild channels (`formatSharedChannelPrompt`)
and gates DMs by `allowed_dm_user_ids` — the model never sees who is DMing, so with
DMs open to everyone (empty allowlist) it would treat strangers as the USER.md owner.
User wants DMs open for everyone AND the model told who's talking: not doable from
config, so patched `internal/discordbot/service.go` with `formatDirectMessagePrompt`
(wraps DM messages as `Direct message / speaker / user_id / content`, same label
style as shared channels). Only upstream file touched outside the original merge
files — keep an eye on it when rebasing upstream. Rebuilt exe 2026-08-14.

## Upstream tracking

Upstream is `eli32-vlc/lumen-agent`; this fork is `FahadBinHussain/lumen-agent`.
All merge work is additive (new internal packages + config fields) — no upstream
files modified except `cmd/element-orion/main.go` (bridge wiring) and
`internal/config/config.go` (new sections). Rebase/merge from upstream stays
straightforward.
