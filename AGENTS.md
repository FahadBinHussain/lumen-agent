# Element Orion fork (FahadBinHussain/lumen-agent) — agent notes

## Admin model catalog (2026-08-14)

- Users cannot select models. `llm.model` = the active model (by catalog name
  or full id); `llm.models` = admin catalog of `{name, model, enabled,
  base_url?, api_key?, api_key_env?}` entries. Admins toggle `enabled` and
  change `llm.model` to switch defaults — no code change, no deleting entries.
  Runtime resolves via `Config.ResolveLLMModel()` + `LLMConfig.ActiveModelProvider()`
  (config.go), used by agent.Run, prompt metadata, heartbeat, dream mode.
- Per-entry provider overrides: each catalog entry may carry its own
  `base_url` and `api_key`/`api_key_env`; the active entry's settings win,
  otherwise the top-level `llm.base_url`/`llm.api_key`/`llm.api_key_env` apply.
  Entries are independent providers — this is how the gateway (mistral,
  sambanova deepseek, groq, cohere, gemini via alchoholpad-litellm.hf.space)
  coexists with the local deepseek-v4-flash server in one catalog.
- Validation: with a catalog present, `llm.model` must match an ENABLED entry
  (by name or model id); every entry needs name+model; at least one enabled;
  duplicate names rejected. Without a catalog, `llm.model` works as before
  (single bare id).
- production.yaml catalog mirrors the gateway's /v1/models list
  (alchoholpad-litellm.hf.space): mistral-large (active default), mistral-small,
  deepseek-v3.2, deepseek-v3.1, llama-3.3-samba, llama-3.3-groq, command-r,
  gemini-3.5-flash — each with `base_url: https://alchoholpad-litellm.hf.space/v1`
  + `api_key_env: LITELLM_API_KEY`. The local deepseek-v4-flash entry uses the
  top-level local base_url/`llama-cpp` key. When adding a model to the gateway,
  add a catalog entry here.

## Persistence: Neon snapshot backup (2026-08-14)

- Render free containers have an EPHEMERAL filesystem — anything written under
  `./.element-orion` (memory shards, session files, heartbeat state, tool
  stores) dies on every deploy/spin-down/recycle. `internal/persist` fixes
  that: it snapshots the session dir into a Neon Postgres table
  (`lumen_snapshots`, per-file rows with sha256) and restores it on a fresh
  box.
- Behavior: local dir stays authoritative while running. On boot, Restore
  ONLY fills a missing/empty dir (never clobbers existing state). Sync upserts
  changed files and deletes stale rows; a coalesced Touch() fires right after
  session/memory writes (via `Service.SetPersistenceToucher`) so a sudden
  container loss costs at most one turn, plus a periodic catch-all (default
  1m) and a best-effort shutdown sync (only when SIGTERM actually arrives —
  don't rely on it).
- Excludes (config + defaulted): sandboxes, whatsapp, incoming-attachments,
  logs. whatsapp.db is already backed up separately via internal/neon.
- **GOTCHA (fixed 2026-08-14, commit 2f8ac45):** `Restore()` originally used
  `len(os.ReadDir(...)) > 0` as its "dir already has state" check — but
  `main.go` creates `<session_dir>/memory` and auditlog creates `logs/`
  BEFORE restore runs, so the dir was never empty and restore silently
  skipped on EVERY boot (no "restored N file(s)" log ever appeared). Symptom:
  messenger deployments crashed with `load cookies: open ... no such file`,
  and each failed boot's shutdown SyncNow deleted Neon rows as stale. The
  dockerignore theory (config/.element-orion junk shipping in the image) was
  WRONG — Render builds from a clean git clone, gitignored files never ship.
  Fix: restore now only skips when the dir contains real FILES (subdirs like
  memory/ logs/ don't count). Diagnosis tool: `render.cmd logs -r
  srv-d9vd3oh42hec738odeg0 --start ... --end ...` with RENDER_API_KEY env —
  the public API has NO log endpoint, but the CLI does.
- Config section `persistence:` (enabled, database_url, database_url_env
  default DATABASE_URL, interval, exclude). Enabled in production.yaml; DSN
  comes from the DATABASE_URL Render env var. Validation fails startup when
  enabled without a resolvable URL — by design, so a missing env var is loud.
- Neon project: `lumen` / aged-fire-12399795, org "Ratul"
  (org-plain-glade-16980612), OWNS ACCOUNT error.503.mail@gmail.com (mainframe
  neon profile), aws-us-west-2, pg 17. Connection URI via
  neon-account.ps1 / mainframe api-key. Never commit the DSN.

## Deploy: Render web service (2026-08-14)

- Service: `lumen` (srv-d9vd3oh42hec738odeg0), workspace "Bayazid's workspace"
  (tea-cu1mom1u0jms738ka280, shared by bayazid10@gmail.com + bayazid190@gmail.com),
  free plan, oregon. URL https://lumen-aqyl.onrender.com, health /api/health.
- Dockerfile multi-stage build (golang:1.25 → debian:bookworm-slim + ffmpeg),
  binds 7860, `serve -config /app/config/production.yaml`. Auto-deploy on main.
- `config/production.yaml` is now TRACKED in git (was gitignored) and is
  secret-free: `discord.bot_token_env: DISCORD_BOT_TOKEN` (added 2026-08-14,
  bot_token fallback if the env var is unset). Render env vars:
  DISCORD_BOT_TOKEN (the real token), LITELLM_API_KEY (alchoholpad@gmail.com HF
  token for https://alchoholpad-litellm.hf.space). GitHub push protection
  (secret scan) blocks any commit containing the token — never commit it.
- First create via `render services create` did NOT auto-trigger a deploy
  (status stayed empty); fix: `POST /v1/services/<id>/deploys` via curl.
- Local Windows runs still use config/lumen.yaml (gitignored).


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

History: per `platform:thread` in memory, persisted to
`<session_dir>/bridge-sessions.json` (symmetry with Discord: same
`agent.CompactHistoryForStorage` compaction on every turn, same file-in-session-dir
pattern, same Neon snapshot backup via internal/persist + `SetPersistenceToucher`,
restored on boot). The old blunt 100-message cap is gone (2026-08-14).

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

## Test threads (2026-08-14)

Hardcoded test targets — use these for cutover test sends only:

- Messenger thread `2637078310061988` (already in `messenger.allowed_thread_ids`)
- Discord channel `1537650032441032765` (AlgoJect — where @murmur hello tests happen)
- WhatsApp contact +880 1711-472629 → JID `8801711472629@s.whatsapp.net` (now gated
  by the new `whatsapp.allowed_jids`, mirror of `messenger.allowed_thread_ids`:
  empty list = allow all; blocked sends log `blocked (not in whatsapp.allowed_jids)`
  and are dropped).

## Cutover readiness vs murmur (2026-08-14 audit)

Audit of murmur's live surface vs this fork (full comparison in session
notes): ~90% covered. Remaining gaps and how they're handled:

- **Send-side thread allowlist (CLOSED 2026-08-14)**: every outgoing Messenger
  send is now gated on `messenger.allowed_thread_ids` — agent replies and the
  notifications endpoint via `Service.send()`, and BNP worker sends via
  `Service.SendMessage`/`EditMessage` (which now return a distinct error
  instead of the empty-ID string, so a blocked thread never fakes the
  cookie-health failure signature). `MessengerThreadAllowed()` (config.go):
  empty list = allow all. WhatsApp got the symmetric `whatsapp.allowed_jids`
  gate (same 2026-08-14), seeded with the test contact only. production.yaml
  carries the current live murmur threads (30738305889116993, 953525124128433,
  2637078310061988). Blocks are log-only + HTTP 200 `{"status":"sent"}`
  (murmur contract — pollers keep working); watch Render logs for "blocked
  (not in messenger.allowed_thread_ids)".
- **Cookie-health auto-refresh (watchdog, READY but not scheduled)**: local
  script `C:\Users\Admin\Downloads\automata\facebook.com\cookie-health.ps1` —
  port of murmur.ps1 Check-CookieHealth/Invoke-CookieRefresh/
  Reset-FailedBnpOutbox: polls dailyBNP Neon (`BNP_DATABASE_URL`, wss via
  bnp-db.mjs) for `send returned empty message ID` failures in a window, runs
  the browserless refresher against the lumen bridge, then resets failed rows
  to pending. Runs only after messenger goes live (needs the BNP worker +
  vault email); schedule via `schtasks /sc minute /mo 10`. Health-gates on
  `MURMUR_HF_SPACE_URL` (default http://127.0.0.1:8791).
- **Dedupe continuity (WIRED 2026-08-14)**: `notify.database_url_env` now
  points at `NOTIFY_DATABASE_URL` = murmur's Neon DSN (steam_seen/game_seen)
  so pollers don't re-send the backlog; set on Render as an env var. lumen's
  own `DATABASE_URL` (persistence + whatsapp sessions) stays the lumen Neon
  project. Value backed up at
  `%APPDATA%\mainframe\state\murmur-neon-database-url.txt`.
- **WhatsApp session carry-over**: NOT portable — murmur's wacli store is a
  different format than lumen's whatsmeow.db. Expect a fresh QR pairing at
  cutover (lumen logs `events.QR`); new sessions persist to lumen Neon.
- **Messenger account**: user plans a different FB account than murmur's —
  `messenger.enabled` stays false. **bridge.secret SET (2026-08-14)**:
  `ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET` on Render = the fahadbinhussain001
  HF profile token (the one the refresher sends as Bearer, verified by sha256).
  The Vercel poller project (murmur, prj_EWeinTGTbfW5iC2bciQ65ZuA4WyZ, owner
  fahadbix@gmail.com) had an old `HF_TOKEN` that matches nothing in mainframe
  — realigned it to the same value via env PATCH (murmur's endpoints never
  checked auth, so this is safe). NOTE: Vercel env GET redacts values
  (`"value": "<redacted>"`, `decrypted:false`) — you cannot hash-verify env
  values via API; trust PATCH `updatedAt` instead. lumen accepts
  `X-HF-Authorization` or `Authorization: Bearer`.
- **Chat history**: symmetric with Discord as of 2026-08-14 — bridge sessions
  use the same token-aware compaction as Discord (`agent.CompactHistoryForStorage`,
  no fixed cap) and persist to `<session_dir>/bridge-sessions.json`, backed up to
  Neon by internal/persist and restored on boot. murmur's Neon `messages` table
  is still not consumed (archival only). `/ai model`/per-thread model selection
  intentionally dropped (single `llm.model` catalog).

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
