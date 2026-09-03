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
- **STATE RESET procedure (learned 2026-08-18)**: to wipe conversational state
  (sessions, memory shards, heartbeat) while keeping the soul files AND a
  bootable container: (1) `POST /v1/services/<id>/suspend` (NOT /stop or
  /pause — both 404; /resume brings it back), (2) `DELETE FROM
  lumen_snapshots WHERE path NOT LIKE '@workspace/%'` via the Neon-under-Proton
  relay, (3) `/resume` (or a fresh deploy). NEVER delete rows while the
  container is running: its 1-minute catch-all sync re-uploads the local dir,
  so a later restore brings EVERYTHING back (bit us: first reset attempt
  "worked" then silently resurrected all 8 rows). **messenger-cookies.json is
  NOT optional state**: `bridge.New` → `messenger.New` errors when the file is
  missing and main exits 1 → deploy `update_failed` / 503 crash loop. It is an
  account linkage like whatsapp_sessions — keep the row, or rebuild it from
  the agent-browser vault
  (`%APPDATA%\mainframe\accounts\agent-browser\cookies\fahadbinhussain001@gmail.com.cookies.json`,
  REQUIRED_COOKIES = `c_user`/`xs`/`datr` → plain `{name:value}` JSON, 163
  bytes) and `INSERT INTO lumen_snapshots (path, data, sha256) VALUES
  ('messenger-cookies.json', decode('<hex>','hex'), '<sha256-hex>')` (schema:
  path text PK, data bytea, sha256 text, updated_at timestamptz). Verify a
  wipe via `/api/health` + deploy status (deploy list wraps objects as
  `{deploy:{...}, cursor:...}`; statuses: build_in_progress →
  update_in_progress → live, or update_failed on instance exit 1).
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
  that's the OFFICIAL Render CLI (github.com/render-oss/cli, binary `render`),
  not npm `render-cli` (0.3.2 = unrelated template engine, a decoy).
- **Render log access (2026-08-17): the public API NOW has logs** — the old
  "public API has NO log endpoint, CLI only" note is STALE. `GET
  https://api.render.com/v1/logs?ownerId=tea-cu1mom1u0jms738ka280&resource=srv-d9vd3oh42hec738odeg0&startTime=...&endTime=...&direction=forward&limit=100`
  with the plain API key (mainframe render profile) returns `{hasMore,
  nextStartTime, nextEndTime, logs[]}`; optional filters level/type/text
  (regex), host, instance, path, method; paginate by passing back
  nextStartTime/nextEndTime; `GET /v1/logs/values` lists label values; WebSocket
  subscribe (`/v1/logs/subscribe`) streams live. This surfaced the 2026-08-17
  TS_AUTHKEY crash: `backend error: invalid key: API key k2bx6Qw2KB11CNTRL not
  valid` -> `Exited with status 1` (see Deploy section). QUIRK (2026-08-17,
  since ~16:00): app-level lines (log.Printf + top-level zerolog info like
  "WhatsApp connected"/"health-watch: ... armed") intermittently stop showing
  in the API while platform sub-logger lines (messagix pings, whatsmeow iq
  debug) keep flowing — treat log-API silence as a pipeline quirk, verify a
  boot via `/api/health` + fresh tailscale magicsock contacts + keepalive
  lines instead of expecting the boot log lines.
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
it is a read-only source for this port. **The murmur HF space
(`fahadbinhussain/murmur`) is PAUSED since 2026-08-15 and must NEVER be deleted
(user rule) — it stays dead but intact as fallback.**

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
  platform, route}`; `platform` defaults to `messenger` (backward compatible),
  `whatsapp` takes the JID in `threadId`. Optional `route` field fans out to
  every channel of a `bridge.routes.<name>` config (messenger primary +
  best-effort extras), unknown route = 400. Optional auth: `bridge.secret` /
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
stripped from the prompt). WhatsApp now mirrors messenger exactly (symmetric
2026-08-17): `/ai` prefix, a reply to one of our OWN sent messages, or a mention
of our jid (group mentions via MentionedJID). Reply detection = whatsmeow
`ContextInfo.StanzaID` matched against a 24h TTL map of outbound message IDs
(`recordSent`/`isRecentSent` in whatsmeow_client.go); mention detection = own
jid (Store.ID) in `ContextInfo.MentionedJID`. WhatsApp mentions ARE stripped
from the prompt (2026-08-17, symmetric with messenger): `CleanMentions` on
WhatsmeowClient resolves each MentionedJID to its contact-store name (full
name, then push name — store syncs from the phone on connect) and removes the
literal `@<name>` tokens, collapsing the leftover double spaces; unresolvable
names leave the text unchanged. Mirrors messenger's offset-based
CleanMentions, which also strips only our own mention. Media-only replies/mentions
stay silent on both. The `/ai` prefix
is stripped; the rest becomes the prompt for the agent runner. No murmur `/ai`
subcommands (models/image) — the fork uses the single `llm.model` config.

Command surface (added 2026-08-18, symmetric with discord slash commands): a
triggered prompt dispatches to the bridge command registry
(`internal/bridge/commands.go`) instead of the agent — same trigger rules as
the agent (`/ai threads`, reply-to-us `@us /memory`, mention, etc.). The
command name may keep its leading "/" or drop it: `/ai threads` and
`/ai /threads` both work (a leading "/" was originally required and `/ai
threads` fell through to the agent — fixed 2026-08-18). Matching is exact
form only: `/ai status`, `/ai /status` dispatch; "status please" or "compact
the code" stay agent prompts. commands:
`/new` (clear this thread's session history, persisted), `/stop` (cancel the
in-flight agent run for this thread; runs are tracked per
`platform:thread` key with a cancelable ctx + seq guard, canceled runs reply
silently instead of "[chat error] context canceled"), `/status` (model,
history length, active run, whatsapp/messenger/discord connectivity),
`/memory` (list the `guild-memory/<platform>/<thread>/` shards for this
thread), `/compact` (force `CompactHistoryForStorage` on this thread's
history now; replies "already compact enough" when nothing changed).
Unrecognized `/...` prompts fall through to the agent. Reply format is
plain text (no discord embeds).

Admin commands (added 2026-08-18): `/threads [page]` lists every thread the
platform account can reach (whatsapp = joined groups + tracked 1:1 chats,
messenger = thread cache nudged by a fetch-tasks sync, discord = guilds +
channels; capped at 25/page, newest-activity first), `/allow <id>` adds a
thread/jid/channel to a RUNTIME allowlist overlay, `/block <id>` removes it,
`/allowlist` shows config + overlay state. The overlay lives at
`<session_dir>/bridge-allowlist.json` (loaded in `New()`, saved on every
mutation, Neon-snapshotted via the persist toucher like bridge-sessions.json)
and is merged with the config allowlists by `Service.threadAllowed()` — the
effective gate for sends AND receives on messenger/whatsapp, so a newly
allowed thread becomes usable without a deploy. Only threads listed in
`bridge.admin_threads` (per-platform; empty = nobody) can run these; a
denied thread gets a one-line "admin command" reply and never falls through
to the agent. production.yaml: admin_threads = the three test channels
(messenger 2637078310061988 / whatsapp 8801911104251@s.whatsapp.net /
discord 1537650032441032765) — keep it that way unless a real thread needs
admin.

History: per `platform:thread` in memory, persisted to
`<session_dir>/bridge-sessions.json` (symmetry with Discord: same
`agent.CompactHistoryForStorage` compaction on every turn, same file-in-session-dir
pattern, same Neon snapshot backup via internal/persist + `SetPersistenceToucher`,
restored on boot). The old blunt 100-message cap is gone (2026-08-14).

Thinking animation + live token streaming (2026-08-18): all three platforms
show a "thinking" placeholder that the final reply edits in place. messenger
keeps the short capped dots animation (Meta's message-edit limits; 4 edits,
500ms apart) — do NOT make messenger's animation longer. whatsapp (bridge
agentRun, `sendReply`) and discord (fork patch `internal/discordbot/thinking.go`,
wired through `processPrompt`'s panic/cancel/error/silent/success paths) have
no practical edit cap, so they show the model's REAL token stream instead of
fake dots: `agent.Runner.chatTurn` streams (llm `StreamChat`, OpenAI-compatible
SSE `chat.completion.chunk` with content + reasoning_content) whenever
`ConversationContext.Streaming` is set, emitting `EventStreamDelta` events
(stream failures fall back to the plain retrying call); bridge/discord
renderers feed those deltas into the placeholder with throttled edits
(whatsapp ~400ms, discord 1s to stay under the ~5/5s edit rate limit),
truncating the preview (whatsapp 1500 chars, discord 1800) and resetting to
"using <tool>..." when a tool call starts (`EventToolStarted`). messenger
never streams (Meta edits can't keep up). Silent turns discard the placeholder
on discord; heartbeat/dream/background prompts skip the animation entirely.

## Platform gotchas

- **Messenger redelivery ghost replies (fixed 2026-08-15, commit 38dac85)**: Meta
  redelivers messages that were queued while the MQTT socket was down — they
  arrive again on reconnect with their ORIGINAL send timestamp. The murmur-style
  boot-time guard in `relay()` (skip msgs older than client start) does NOT catch
  them when the original send happened after boot. Symptom: the bot replies to an
  already-answered message hours later (user saw "who are u" answered 3x — 23:25,
  00:13, 00:31 local, each delivery ~3s after a socket drop). Fix: `relay()`
  dedups on `MessageID` (map with 24h retention, pruned on growth >500). Keep
  both guards — the time guard still protects against fresh-boot history delivery.
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
    edit → POST ack back). env contract: `BNP_MESSENGER_OUTBOX_URL/TOKEN`,
    `BNP_ROUTE` (default "bnp"), `BNP_MESSENGER_POLL_SECONDS`,
    `BNP_MESSENGER_CLAIM_LIMIT`, `BNP_MESSENGER_REQUEST_TIMEOUT_SECONDS`.
    wired in `bridge.Service.Run`, starts only when `messenger.enabled` is true
    AND the env vars are set (worker logs "disabled" otherwise). sender
    interface (`RouteSender`) defined in `internal/bnp` to avoid an import
    cycle; `Service.SendRoute`/`EditRoute` implement it. the push endpoint
    `POST /api/automation/notifications` (with `bridge.secret` auth) remains
    the alternative for sources that can push.
  - routes fanout (added 2026-08-18, commit f0a8622 + routes standardization):
    `bridge.routes.<name>` = list of `{platform, thread_id|jid|channel_id}`
    channels (messenger/whatsapp/discord; validated at boot). the BNP worker
    sends outbox items to its route's channels: the messenger channel is the
    PRIMARY (its message ID feeds the ack contract, its failure fails the
    item), every other channel is best-effort (logged, never fails the ack);
    edits (edit_pending items) replace the primary messenger message AND
    update every other channel: whatsapp edits IN PLACE when the mirror's
    message ID was recorded (whatsmeow BuildEdit, 20-min edit window;
    mapping keyed "messengerID|jid" consumed on first edit, dropped when the
    window is missed) and falls back to a fresh-message mirror otherwise;
    discord edits IN PLACE too via REST PATCH (mapping keyed
    "messengerID|channelID", `DiscordHealthClient.EditPlainText`), same
    fresh-message fallback. the original bug: the whatsapp group kept stale
    "detected" text while messenger had the edited "published" text
    (2026-08-18, commit 0e5a88a fresh-mirror, then 28a87e1 whatsapp
    BuildEdit + 310d91b discord PATCH in-place edits; live-verified
    2026-08-18 with a fake detected→published pair through the real worker
    on the production bnp route — the worker follows BNP_ROUTE, NOT the
    route name in the fake item, and fake items go to the bnp route's
    channels). the notifications
    endpoint also accepts an optional `route` body field to fan out instead
    of single platform/threadId (unknown route = 400). adding a channel =
    edit `bridge.routes` in production.yaml + deploy, no code. current bnp
    route: messenger thread 984803114200952 + whatsapp group
    120363409684314037@g.us (user's "ai" group). group JID lookup: `GET
    /api/whatsapp/groups` (secret-gated) → whatsmeow `GetJoinedGroups()`,
    returns `{groups:[{id, name}]}` — group ids are `@g.us` JIDs, names are
    structs (use `.name`). dead env vars: BNP_MESSENGER_THREAD_ID and
    BNP_WHATSAPP_THREAD_ID were removed from Render 2026-08-18 — the thread
    and jid live in the route config now.
  - notifier kill-switches (added 2026-08-14): `bridge.notifications_enabled`
    and `bridge.bnp_enabled` (both default true in code; set FALSE in
    config/lumen.yaml). notifications_enabled=false unmounts ONLY the
    `/api/automation/notifications` handler — `/api/cookies/upload` is NOT
    gated (it's ops-critical for the browserless cookie refresher and stays
    mounted whenever messenger.enabled is true). bnp_enabled=false skips the
    BNP worker goroutine. production.yaml (Render) has notifications_enabled
    + bnp_enabled = true since commit 099a6e1 — the BNP worker and
    notifications endpoint ARE live on lumen; local lumen.yaml keeps them
    off.
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
  - **crack_watch poller (added 2026-08-24)**: `internal/notify/crackwatch.go`
    polls `https://www.reddit.com/r/CrackWatch/.rss` (Atom) for scene releases.
    Keeps only `Game-GROUP` posts (regex `-([A-Z0-9]{2,10})$` — drops "Daily
    Releases (...)" digests, stickies, and meta threads), dedupes against Neon
    `crack_seen`, pushes to the webhook as source `crackwatch` with
    dedupeKey = reddit post URL. Config `notify.crack_watch.{enabled,interval
    (default 5m),feed_url,thread_ids,webhook_url}`. Retries the feed fetch 3x
    (5/10/15s backoff) — reddit's .rss intermittently resets connections from
    datacenter IPs. Enabled=false everywhere by default; flip in the config
    that's live on the target box + set thread_ids.
  - **cs.rin.ru feeds (researched 2026-08-24, NOT added — future option)**: the
    forum's phpBB `feed.php` is whitelisted (no login, no JS-challenge) and is
    the only structured endpoint. Working modes, all Atom: `feed.php` (global
    recent posts), `feed.php?f=<id>` (per-forum), `feed.php?t=<id>`
    (per-topic), `feed.php?mode=forums` (forum list), `feed.php?mode=topics&f=<id>`
    (topics in a forum), `feed.php?mode=topics_new` (new topics globally).
    Access wrapper already exists at `C:\Users\Admin\Downloads\automata\cs.rin.ru\`
    (csrin-session.ps1 token bootstrap, csrin-feed.ps1, csrin-search.ps1,
    csrin-thread.ps1). Candidate uses if ever wanted: per-game thread updates
    (e.g. GTA V Legacy t=67450), new-topics stream (`mode=topics_new`), or a
    `[CRACKED]`-tag tracker on the Main Forum (f=10). Verdict from research:
    mostly duplicates r/CrackWatch (already wired as `crack_watch`); only
    cs.rin.ru-specific value is version-level detail + crack-status tags per
    game thread. Deferred unless a concrete use appears.
  - **neon_usage encrypted export (added 2026-08-24)**: when an org's
    consumption passes `warning_hours`, `checkNeonUsage` now ALSO dumps every
    project in that org (pg_dump → gzip → openssl aes-256) and force-pushes the
    encrypted files to the `exports` branch of the lumen repo (config
    `notify.neon_usage.export.*`). Runs on `export_interval` (default 24h) —
    NOT every hourly poll and NOT deduped like the warning (the warning fires
    once per reset period; the export re-dumps at most once per interval while
    over threshold so the backup stays fresh). Single commit per cycle (fresh
    `git init -b exports` + force-push = flat history, 1 commit always, old
    blobs unreachable → GitHub GCs). Env vars on
    Render: `LUMEN_EXPORT_KEY` (openssl passphrase; decrypt = `openssl enc -d
    -aes-256-cbc -pbkdf2 -pass env:LUMEN_EXPORT_KEY`), `LUMEN_EXPORT_GITHUB_TOKEN`
    (fahadbinhussain@outlook.com fine-grained PAT scoped to lumen-agent only,
    contents:write). Dockerfile now installs
    `postgresql-client git openssl` (pg_dump + git needed). code:
    `internal/notify/neonexport.go` (`exportNeonOrg` per over-threshold org,
    `pushExports` batched single commit). Recovery of an encrypted export: key
    copy at `C:\Users\Admin\Downloads\lumen-export-key.txt`. File names are
    `<project-id>.sql.gz.enc` under `backups/`.
  - **neon watcher covers ALL accounts (2026-08-29)**: `neon_usage.api_key_env`
    lists all 19 mainframe Neon API keys (env vars `NEON_API_KEY_01`..`NEON_API_KEY_19`,
    ordered by profile email, mirroring neon-hours-table.ps1). The old single
    `NEON_ORG_API_KEY` (error.503.mail@/Ratul only) was replaced — that asymmetry
    meant e.g. Daily-BNP (ahmedtouhid88@, 110%) never warned. Any org over
    `warning_hours` now warns + exports, across every account. Add a new Neon
    account = new env var on Render + append to this list.
  - **neon checks ALL THREE quotas (2026-08-29)**: compute (CU-h, `warning_hours`,
    default 90 of 100), storage (`warning_storage_pct`, default 80 of 0.5 GB),
    and egress (`warning_egress_pct`, default 80 of 5 GB). Storage+egress read
    from the SAME org consumption endpoint (`peak_data_storage`/`data_transfer`
    on the last period), so no extra API calls. Each metric dedupes once per
    project per reset period (separate `storage:`/`egress:` keys in neonState).
    e.g. the-daily-times was 5.15 GB egress → fires the egress warning while
    compute (56 CU-h) and storage (46 MB) stay quiet. NOTE: egress/storage are
    PER-PROJECT on free, but the org consumption endpoint aggregates the whole
    org (verified 2026-08-29: a fresh project on a quota-dead account starts at
    0 transfer — see automata/neon.com/AGENTS.md). So an org-level egress warning
    same account resets the bucket.
  - **pending notification queue (added 2026-08-29)**: when the mouth is dead
    (messenger/whatsapp/discord not connected or Render free-tier asleep),
    `POST /api/automation/notifications` no longer drops the message with a
    fake 200 — it queues to Neon `pending_notifications` (durable, survives
    free-tier restarts via `internal/neon`, same DB as `whatsapp_sessions`).
    Each row stores platform/thread/route/title/message/dedupe_key/source/url;
    dedupes on `dedupe_key` so a retried poller doesn't double-queue. Drain
    happens on `health_watch` alive (`internal/bridge/healthwatch.go` calls
    `drainPending` when a platform flips dead→alive) and every 5m as a safety
    net (`internal/bridge/service.go` periodic ticker + 20s boot drain). Query
    the backlog via `GET /api/automation/notifications/pending` (secret-gated
    same as the POST, returns `{pending:[], count}`).
  - model-catalog listing (/ai models etc.) intentionally dropped — the fork
    uses the single `llm.model` config.
- Pre-existing test failures on this machine (NOT caused by the merge, verified):
  - `internal/discordbot` heartbeat test hardcodes Linux path `/workspace/lumen/...`
  - `internal/agent` `TestSystemPromptIncludesSharedChannelSilenceGuidance`
    expects an upstream silence-guidance string the fork prompt no longer contains
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
- WhatsApp contact +880 1911-104251 → JID `8801911104251@s.whatsapp.net` (now gated
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
- **Cookie-health auto-refresh (watchdog, SCHEDULED 2026-08-17)**: local
  script `C:\Users\Admin\Downloads\automata\facebook.com\cookie-health.ps1` —
  port of murmur.ps1 Check-CookieHealth/Invoke-CookieRefresh/
  Reset-FailedBnpOutbox: polls dailyBNP Neon (`BNP_DATABASE_URL`, wss via
  bnp-db.mjs) for `send returned empty message ID` failures in a window, runs
  the browserless refresher against the lumen bridge, then resets failed rows
  to pending. Runs only after messenger goes live (needs the BNP worker +
  vault email). Task `lumen-cookie-health` (scheduled task) recreated
  2026-08-17 with the murmur pattern (LogonTrigger + PT30M repetition,
  InteractiveToken, HighestAvailable, IgnoreNew) — same behavior as the
  `murmur` task, NOT the old schtasks /sc minute form. On-demand verify:
  `schtasks /run /tn "lumen-cookie-health"`; log `$env:TEMP\lumen-cookie-health.log`;
  Next Run shows N/A (logon-only trigger fires at next logon). Health-gates on
  `MURMUR_HF_SPACE_URL` (default http://127.0.0.1:8791).
- **Dedupe continuity (SELF-CONTAINED 2026-08-30)**: `notify.database_url_env`
  = `DATABASE_URL` — lumen's OWN Neon. `steam_seen`/`game_seen`/`crack_seen`
  dedupe tables, supabase app_state, persistence snapshots, whatsapp sessions,
  AND the pending_notifications queue ALL live in the single lumen Neon project.
  `NOTIFY_DATABASE_URL` was removed from Render 2026-08-30 (it pointed at an
  external dead project that hard-failed boot when over quota). On the switch
  the dedupe tables were seeded with the then-current feed GUIDs so nothing
  re-sends; `internal/notify/notify.go` also tolerates a dead dedupe DB
  (steam/free-games/crack_watch run without dedupe rather than crash boot).
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

## Identity-first system prompt (2026-08-17 fork patch)

Upstream `baseSystemPrompt` (internal/agent/prompt_context.go) hardcodes "You are
Element Orion, a companion replying through a Discord bot." plus an "Agent name:
Element Orion" metadata fallback — so even with IDENTITY.md/USER.md/SOUL.md in the
workspace, the model kept self-identifying as Element Orion (repro: `/ai who are
you` answered "I'm Element Orion" AND offered the bootstrap ritual, contradicting
SOUL.md's "the ritual is complete, never mention it" — proof the identity sections
weren't winning against the system prompt). Patch: when IDENTITY.md exists at the
workspace root, `systemPrompt()` replaces the hardcoded first line with an
identity-yields instruction, and `runtimeMetadataLines()` uses the `- name:` line
from IDENTITY.md for "Agent name" instead of the Element Orion fallback. Keep an
eye on this when rebasing upstream.

## Platform symmetry audit (2026-08-17)

All three platforms (Discord, WhatsApp, Messenger) share the same agent Runner,
so identity files, memory prompts, and history compaction are symmetric by
construction. Per-platform persistence:

- **Identity files** (IDENTITY.md/USER.md/SOUL.md): workspace root, loaded into
  every platform's system prompt. ✓
- **Session history**: Discord `session-*.json` ↔ bridge `bridge-sessions.json`,
  both in the session dir, both backed up by internal/persist. ✓
- **Memory shards**: Discord appends each exchange to the half-day shard
  (`AppendToMemoryShard` in service.go). The bridge did NOT — WhatsApp/Messenger
  prompts already read `guild-memory/<platform>/<thread>/` shards but nothing
  ever wrote them. Fixed 2026-08-17 (commit TBD): bridge `agentRun` now appends
  the exchange to the same shared memory root via
  `agent.SharedConversationMemoryRoot` + `AppendToMemoryShard`, symmetric with
  Discord's guild-channel path. ✓
- **Discord DM memory isolation (2026-08-17, commit TBD)**: upstream 1:1 DMs
  read AND wrote the GLOBAL `memory/` dir (MEMORY.md + shards) — fine with a
  single `allowed_dm_user_ids` partner, but with DMs open to everyone every DM
  partner shared one memory bucket (cross-DM bleed). Now 1:1 DM shards live at
  `<session_dir>/guild-memory/discord-dm/<dm-channel-id>/` — read side
  `workspacePromptSections` (IsDirectMessage branch), write side the
  `state.Key.GuildID == ""` branch of `appendMemoryShard` (falls back to
  global MemoryDir when SessionDir is unset), `/memory` report shows the same
  root. Global `MEMORY.md` stays loaded in DMs (agent-curated long-term memory
  is still global). Group DMs were already isolated via `group-dm-memory/`.
  Old global DM shards are orphaned (not read anymore) — copy them manually
  if needed. Same root taxonomy as bridge threads (`guild-memory/<platform>/<thread>`).
- **Shard speaker label**: `AppendToMemoryShard` now takes the assistant label
  (identity name from IDENTITY.md via `agent.IdentityDisplayName`), so shards
  say "**Kite:**" not "**Element Orion:**" on all platforms. ✓

## Cross-platform health notifications (2026-08-17)

In-container watch (`internal/bridge/healthwatch.go`, config `bridge.health_watch:`):
when a platform dies/recovers it tells the OTHER platforms' test channels:
whatsapp ↔ messenger thread 2637078310061988 ↔ whatsapp jid
8801911104251@s.whatsapp.net, discord → both of those, and whatsapp/messenger
deaths also copy to the discord channel (`discord_channel_id`, currently
1537650032441032765 in AlgoJect) when set. So the mesh is symmetric: each
platform's alerts land on the other two. Discord connection state is tracked
in-process (`setConnected` via discordgo Ready/Resumed/Disconnect handlers +
`IsConnected()` on the discordbot Service, wired into the bridge with
`SetDiscord`), so a discord gateway drop fires the same dead/alive alerts
(discordgo auto-reconnects, so dead only lasts until the gateway is back).
Sends go through the normal allowlist-gated
`send()` path, so alerts to unlisted channels are dropped with a log line
(discord alerts use `SendPlainText` directly on the channel id).

Semantics (all covered by unit tests):
- **Arming**: no notification until the platform has been connected at least
  once (boot/deploy spam guard — a fresh deploy never spams while both
  platforms come up).
- **Dead debounce** (`dead_after`, default 45s): a dead state must persist
  before "X is dead" fires, so brief reconnect blips AND the intentional
  messenger cookie reload (ReloadCookies sets connected=false for a few
  seconds) stay silent.
- **Cooldown** (`min_notify_interval`, default 2m): caps flap spam — dead→alive
  →dead cycles within the window only send once. **Pending-alive fix
  (2026-08-18)**: recovery inside the cooldown now leaves the alive
  notification PENDING and it fires as soon as the cooldown elapses while the
  platform stays up — the old code reset lastDead on the suppressed recovery,
  so quick come-backs (the common case, whatsapp reconnects fast) swallowed
  the "alive again" message forever (user saw dead msgs but never alive msgs).
- Watch loop runs inside the container (`bridge.health_watch.enabled`), so the
  whatsapp-dead alert still has a working messenger channel when the laptop
  (the whatsapp tailnet route) is down. Pure notification — nothing here
  heals anything; the platforms auto-retry on their own.

Platform state plumbing: `messenger.Client.IsConnected()` added (messagix has no
disconnect event — Ready/Reconnected set connected=true, Event_SocketError /
Event_PermanentError set false; ReloadCookies resets it at the start).
`whatsapp.WhatsmeowClient.IsConnected()` is now mutex-guarded (was a racy plain
bool). Both are polled, not event-driven.

## WhatsApp pairing gotchas (2026-08-17)

- **RESOLVED 2026-08-17**: paired via PairPhone code flow (account `REDACTED_PHONE`
  / `REDACTED_PHONE`) and transferred to Render through Neon
  (`POST /api/whatsapp/session/upload`); QR page reports `"status":"paired"`.
  Commit `c9d4ae8` carries the fixes. Old number `+8801911104251` stays
  hard-blocked — never retry it.
- **Pairing identity fixes (the actual root cause)**: (1) whatsmeow `Client.QRClientType`
  was unset → QR advertised client type `9` (OtherWebClient) and the server
  rejected phone confirms ("unnamed device" + "couldn't link device"). Must be
  `PairClientChrome`. (2) `PairPhone` display name must be `Browser (OS)`
  format (`"Chrome (Windows)"`); `"lumen"` → server returns `400 bad-request`
  on the code flow. Set both in `internal/whatsapp/whatsmeow_client.go`.
- QR page button hardcodes the pairing number (`notifications.go` line ~264) —
  update it when the target account changes (the pair endpoint itself takes
  the phone in the body).
- Client must be current: whatsmeow <2026-06-22 breaks pairing (server now
  expects passkey + client-props handshake). Pinned Aug 16 build
  (`fb386f152837`) + mautrix-go v0.30.0 — both required together (util v0.10.0).
- **Burned store identity**: `whatsmeow.db` device ID survives every restart and
  got flagged after failed attempts. Symptom: scan produces ZERO log activity.
  Fix: stop instance, move `whatsmeow.db` aside, restart for a fresh device ID.
- Local debugging stack: `C:\tmp\lumen-mini.yaml` (persistence disabled, whatsapp
  proxy = local socks5 127.0.0.1:1080, bridge 127.0.0.1:8793), launcher
  `C:\tmp\lumen-local.cmd` (clears DATABASE_URL — local pgx→Neon hangs), log
  `C:\tmp\lumen-local.log`, store `C:\tmp\.element-orion\whatsapp`.
- **WhatsApp egress via tailscale exit node (2026-08-30, replaces
  socks5-proxy + sockschain sidecar)**: Render datacenter IPs get TLS-blocked
  by WhatsApp, so the websocket egresses from the home IP. Previously that
  meant a laptop socks5-proxy (100.76.10.50:1080) + a `cmd/sockschain` sidecar
  chained through tailscale's userspace socks — since 2026-08-30 it uses a
  NATIVE tailscale EXIT NODE: laptop-main advertises
  `--advertise-exit-node` (Windows needs `IPEnableRouter=1` registry + reboot;
  approved in the admin console), the container's tailscaled runs
  `--tun=userspace-networking --socks5-server=127.0.0.1:1055` and
  `tailscale up ... --exit-node=100.76.10.50`, and whatsmeow just points at
  tailscale's OWN socks server: `WHATSAPP_PROXY_URL=socks5://127.0.0.1:1055`.
  Chain: whatsmeow → tailscaled socks (1055) → exit node laptop-main → WhatsApp
  from home IP. No socks5-proxy.exe, no sockschain, no SOCKS_CHAIN_UPSTREAM
  (env var removed 2026-08-30). AUTO-HEAL: whatsmeow retries forever in the
  container (disconnect → infinite reconnect loop with backoff), tailscale
  rejoins via authkey on any container restart, so a dead laptop / tailnet
  path heals once the laptop returns. Non-auto-heal cases: WhatsApp logging
  the device out (needs manual phone re-pair, watcher could only alert).
  The cookie-health watch exists only because messenger cookie refresh is an
  ACTIVE laptop-side heal (agent-browser vault push); WhatsApp is
  passive-wait, so no watch. bridge `/api/health` returns bare "ok" — it does
  NOT expose WhatsApp state, and `WhatsmeowClient.IsConnected()` is not
  surfaced anywhere. `TS_AUTHKEY` = the fleet reusable authkey (mainframe
  tailscale profile; since 2026-08-17 the validated vault key, also saved to
  the mainframe tailscale profile's authkey.txt; the old fleet key expired
  and every fresh boot failed with `invalid key: API key k2bx6Qw2KB11CNTRL
  not valid` -> `Exited with status 1` -> deploy update_failed/crash loop,
   while the old instance kept serving); GOTCHA: `tailscale up` failing = exit
  1 via `set -e` in entrypoint.sh; fix = PUT a valid key via `PUT
  /v1/services/<id>/env-vars/TS_AUTHKEY` then redeploy (manual deploy POST;
  autodeploy on push does NOT fire). **WhatsApp health distinction (2026-09-03)**:
  health-watch now differentiates `is dead (auto-retrying...)` vs
  `is logged out (needs phone re-pair via QR - not auto-recovering)` — logged-out
  is fatal (401 from WhatsApp, store deleted, `whatsapp_is logged out` requires
  QR at `GET /api/whatsapp/qr`). Logged-out arms immediately on fresh boots
  (transient dead still needs one prior alive), and `whatsmeow_client` now
  auto-creates a fresh device on `LoggedOut` (previously `Connect` failed with
  `invalid use of deleted device` and never produced a QR; now `NewWhatsmeowClient`
  skips deleted devices and `Connect` recreates the client). `LoggedOut` now
  also triggers `scheduleReconnect` (15s) like `Disconnected`. `--state=mem` makes every container node
  EPHEMERAL regardless of key type (verified: tailscaled help says mem state
  registers as ephemeral). entrypoint.sh runs tailscaled
  `--tun=userspace-networking --socks5-server=127.0.0.1:1055 --state=mem` then
  `tailscale up --authkey=... --hostname=lumen-render --accept-dns=false
  --exit-node=100.76.10.50 --timeout=40s`. GOTCHA: `--accept-dns` is a
  `tailscale up` flag, NOT a tailscaled flag — passing it to tailscaled makes
  it print usage and exit 1 (deploy update_failed; fixed 2026-08-17, commit
  2a0ac08). Verify: logs `entrypoint: tailscale userspace node up (<ip>),
  exit node 100.76.10.50` + `WhatsApp connected`; tailnet shows `lumen-render`
  (linux, active).
- **Log-noise flood from the SOCKS listener (resolved 2026-08-17, commits
  cdb00e1 + b13d34b)**: the container logs were drowned by ~1-2/s
  `[ERR] socks: Unsupported SOCKS version: [72]` + `serve 127.0.0.1:PORT: ...`
  pairs starting the first second of boot, making Render logs useless
  (log API caps at ~300-500 lines ≈ a few minutes of flood). Root cause:
  tailscaled's netstack PORT DISCOVERY probes the container's own listeners
  with HTTP-ish first bytes (0x48 = 'H') — every probe hitting a SOCKS
  listener gets rejected and logged by tailscaled's 1055 socks server.
  Harmless rejects, cosmetic problem. Fix: entrypoint.sh pipes tailscaled's
  stderr through `grep --line-buffered -vE 'Unsupported SOCKS version|
  incompatible SOCKS version|socks5: client connection failed|peerapi: unknown
  peer|RATELIMIT'`. Verify after deploys: `render-cli logs` shows no
  socks-noise lines.
- **Neon from local while Proton VPN is on** (2026-08-17): Proton's WFP filter
  driver blocks direct psql/pgx traffic even with host routes added, and its
  client rewrites ServiceSettings.json split-tunnel edits on restart — don't
  fight the config file. Working bypass: route through v2rayN's mihomo core
  (socks 127.0.0.1:7891). mihomo runs in `mode: global`, so `GLOBAL` selector
  must point at `PROXY` (PUT http://127.0.0.1:9090/proxies/GLOBAL
  {"name":"PROXY"}) or traffic egresses DIRECT and gets blocked. Then forward
  psql via the one-shot socks relay `C:\tmp\socks5-fwd.ps1` (listens
  127.0.0.1:5433, accepts multiple connections, CopyToAsync — never Start-Job,
  streams can't cross runspaces). psql hits 127.0.0.1:5433 and MUST pass the
  endpoint ID since SNI is lost: `?options=endpoint%3Dep-divine-sunset-a67l3n4m`.
  Neon project `lumen` = `aged-fire-12399795`, branch `br-damp-sea-a6c1ccfj`,
  db `neondb`, user `neondb_owner`.
- **Identity files (fixed 2026-08-17, commit TBD)**: the prompt loader reads
  `IDENTITY.md`/`USER.md`/`SOUL.md` from the WORKSPACE ROOT (`/app/config/` on
  Render), not from the session/memory dir — they were originally backed up as
  `memory/*.md` rows and restored into `.element-orion/memory/` where nothing
  ever read them (the bot kept identifying as "Element Orion"). persist now
  (1) backs up the workspace-root identity files under the `@workspace/` path
  prefix (Restore writes them to the workspace root, only when absent locally;
  Sync upserts changed content but NEVER deletes @workspace/ rows — the
  container has no local copy until a boot restores them, so auto-deleting
  them wipes the seed before the next restore sees it; that race actually
  bit us on 2026-08-17: the first 83940e0 deploy's sync deleted freshly
  inserted rows within a minute), and (2) permanently excludes the legacy
  `memory/SOUL.md`, `memory/IDENTITY.md`, `memory/USER.md` paths from
  restore+sync so those rows self-delete on the first sync. To remove an
  identity file for good, delete its @workspace/ row in Neon manually. The local
  files live at `config/{IDENTITY,USER,SOUL}.md` (gitignored). To push a
  changed identity: update the local file, upsert the `@workspace/<name>` row
  in Neon (`INSERT ... ON CONFLICT (path) DO UPDATE`, base64 + sha256, via the
  Neon-under-Proton relay), and trigger a fresh deploy; verify via logs
  `persist: restored N file(s) from snapshot` + the agent prompt no longer
  saying "Element Orion".
- Local pairing path: kill local instance AFTER pairing before deploying Render
  (same identity dual-connect conflict); transfer session via
  `POST /api/whatsapp/session/upload` (bridge secret auth) → Neon restore on boot.

## Upstream tracking

Upstream is `eli32-vlc/lumen-agent`; this fork is `FahadBinHussain/lumen-agent`.
All merge work is additive (new internal packages + config fields) — no upstream
files modified except `cmd/element-orion/main.go` (bridge wiring) and
`internal/config/config.go` (new sections). Rebase/merge from upstream stays
straightforward.
