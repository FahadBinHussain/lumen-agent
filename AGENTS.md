# Element Orion fork (FahadBinHussain/lumen-agent) — agent notes

## Proton VPN is forbidden ground for lumen (2026-10-09, user rule)

- NEVER touch Proton VPN for anything lumen-related: no toggling it, no
  switching/reconnecting servers, no editing split-tunnel/kill-switch
  settings, no "try turning Proton off/on" diagnostics, and do not offer it
  to the user as a fix option either. Proton state is user-owned and outside
  lumen's control.
- Lumen must be PROTON-AGNOSTIC: it works — or fails loudly and
  self-recovers — whether Proton is on, off, or mid-server-switch. Proton
  flapping (4 server switches on 2026-10-09 alone) is a NORMAL
  environmental condition, same class as a Render restart or a tailscale
  blip: design around it, never try to eliminate it by touching Proton.
- When Proton's state breaks a lumen path, the fix MUST come from the
  lumen side. The recurring one: whatsapp tail-exit egress
  (`whatsmeow -> tailscaled socks 1055 -> laptop-main exit -> internet`)
  black-holes with `general SOCKS server failure` whenever Proton re-keys.
  Acceptable fixes = lumen-side alternatives (retry windows, a different
  exit node, an egress path that does not ride Proton, ...). "Switch or
  reconnect Proton" is not an acceptable answer in any form.
- Read-only correlation notes are fine (e.g. "outage lines up with the
  08:40 Proton server switch") — report the fact, then solve it from the
  lumen side anyway.

## Discord account bans: read the rulebook before touching anything discord (2026-10-16, user rule)

- discord automation has a hard-earned rulebook on this machine: ban
  triggers, volume/loop guardrails, live incident writeups (scripted API
  writes killing sessions after a single message, an alt account ending up
  in discord's forced password-reset state), and per-account state notes.
  BEFORE any discord-side work — user-mode token auth, sending, reacting,
  account switching, session debugging — read:

      %USERPROFILE%\Downloads\automata-private\discord.com\AGENTS.md

- short version, so a run that cannot read that file still fails safe:
  user-token use outside the official client violates discord's ToS and is
  bannable. What actually gets accounts actioned are PATTERNS — volume,
  loops, replicated or bulk messages, foreign-IP / proxy / VPN egress,
  main accounts. Single low-volume one-off actions on alt accounts are the
  accepted class; anything batch-shaped is not.
- never add loops, bulk sends, or stripped rate-limit waits "to make it
  faster" — the runtime stays at conversation pace, one message at a time.
- keep discord traffic on the machine's normal IP — never a VPN, proxy, or
  datacenter egress path (see the Proton rule above).
- token hygiene is absolute: never print, log, or paste a token or cookie
  outside the vault — not into issues, not into logs.
- when an account dies (401s, "session invalid", forced password reset):
  STOP, then check the rulebook's account-state + incident sections before
  retrying anything against that account.

## Deployment fixes (2026-09-23)

- Production's active model is `deepseek-v4.1-flash` = `deepseek-v4.1-flash-1017` via
  LiteLLM gateway (`https://alchoholpad-litellm.hf.space/v1`, key in Render env `LITELLM_API_KEY`,
  `api_type: openai`, `reasoning_effort: low`) since 2026-10-26 — mirroring dailybnp's live lane.
  The opencode zen free lane (`mimo` / `mimo-v2.6-flash-free`) suffered rate-limiting/exhaustion
  leaving chat turns stuck at "thinking.". Kept in catalog as disabled fallback.
- Windows test runs must not assume `/bin/zsh`; the exec tests use `pwsh` on
  Windows while production Linux keeps its configured shell.
- The Messenger cookie upload endpoint accepts the mainframe cookie-vault
  snapshot as a plain `{name:value}` JSON map and reloads the connection with a
  detached context. WhatsApp still requires a phone-side QR/linking action when
  the persisted Neon session has no device JID.

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
  coexists with the active Atria ASI provider (api.atria-asi.ai) in one
  catalog.
- Validation: with a catalog present, `llm.model` must match an ENABLED entry
  (by name or model id); every entry needs name+model; at least one enabled;
  duplicate names rejected. Without a catalog, `llm.model` works as before
  (single bare id).
- production.yaml catalog: active model is `mimo` = `mimo-v2.6-flash-free`
  via `https://opencode.ai/zen/v1` with `api_key: public` on the zen free lane
  (`api_type: zen`). Warning: `llm.api_type` is a TOP-LEVEL switch, not
  per-entry — switching the active model between different transports means
  changing `llm.model` AND `llm.api_type` + `llm.base_url` together (zen is
  usable only when the model is `mimo-v2.6-flash-free`; atria-dawn needs
  `api_type: openai`). `atria-dawn` = `Atria-Dawn-Preview`
  via `https://api.atria-asi.ai/v1` with `api_key_env: ATRIA_API_KEY` (Render
  env, never commit) is still in the catalog ENABLED as the fallback. The
  disabled gateway entries mirror the gateway's
  /v1/models list (alchoholpad-litellm.hf.space): mistral-large, mistral-small,
  deepseek-v3.2, deepseek-v3.1, llama-3.3-samba, llama-3.3-groq, command-r,
  gemini-3.5-flash - each with `base_url: https://alchoholpad-litellm.hf.space/v1`
  + `api_key_env: LITELLM_API_KEY`. When adding a model to a provider,
  add a catalog entry here. The local deepseek-v4-flash entry
  (`49.205.178.204:11434` llama-cpp box) was REMOVED 2026-10-02 - that box is
  dead; never reintroduce that address.

## Zen chat lane: `llm.api_type: zen` (2026-10-16)

- `internal/llm/zen.go` (tests `zen_test.go`) is the production port of
  diit-chatbot's `src/zen.mjs`: chat through the opencode zen free lane,
  selected with top-level `llm.api_type: zen`. Why: kite's Discord chat was
  erroring after Atria quota exhaustion; the zen lane is the free-lane
  workaround, same as diit.
- **The gate recipe was re-bisected 2026-10-16 (see the corrected gate
  model in the Vision section below). There is NO prompt-prefix
  requirement: the deployed shape is the main-agent branch — a body
  carrying a `tools` array + `stream:true` + a correctly generated
  opencode session id. The old byte-exact prompt capture is gone
  (deleted `zengate-title-prompt.txt` + its `.gitattributes` pin); the
  2026-10-14 "attested client required" verdict was wrong — random `ses_`
  ids were the actual breakage.**
- The lane constraints (all enforced in `buildZenTurn`/`call`, do not
  "simplify" them away):
  1. system message = Kite's own system prompt + lane rules + folded
     dialogue + final reminder (content is free on this branch);
  2. `stream: true` REQUIRED on the wire (Chat and StreamChat both SSE);
  3. the body carries the embedded 12-tool main-agent array
     (`zengate.Tools()`) + `tool_choice: "none"`; `Request.Tools` is DROPPED
     (never forwarded). The lane rules tell the model not to emit tool-call
     syntax as text (a hallucinated `send_discord_message` shown verbatim
     would be a bug); `<NO_REPLY>`/`<chunk>` are text markers and still
     work;
  4. exactly ONE live user message: history is folded into the system tail
     as `User:`/`Assistant:`/`Tool result (...):` lines (assistant tool-call
     notes inline, 2000-rune cap per line, 300 for tool args).
  Consequence: multi-round tool loops cannot happen on this lane — one
  model call per turn, reply text = the delivered message.
- Request body is pinned to the captured shape: `model`, `messages`,
  `tools` (embedded array), `tool_choice: "none"`, `stream: true`,
  `stream_options.include_usage`, `max_completion_tokens: 32000`,
  `store: false`. No `temperature`/`max_tokens`. Identity headers:
  `Bearer public`, UA `opencode/latest/2.0.23/cli`, `x-opencode-client:
  cli`, a fresh 40-hex `x-opencode-project`, ONE freshly generated `ses_` id
  copied into all four session headers (`x-opencode-session`,
  `x-opencode-session-id`, `x-session-affinity`, `x-session-id`), fresh
  `b3`/`traceparent`. NO `x-opencode-request` header (the captured client
  sends none on this route).
- Retry cadence (port of diit): 4 attempts; blip/403 backoff 1/2/4s + ≤400ms
  jitter; 429 backoff 5/12/25s; ~180s budget, 120s per attempt. Final error
  is loud: status + body + type + attempt count + the ambiguity caveat. The
  error string keeps the `API error (%d)` shape so `llm.IsRetriableError`
  classifies it identically everywhere (403 → NOT retriable at the agent
  layer, 429/5xx → retriable).
- **403 FreeTierError is ambiguous by design** (verified again 2026-10-16):
  it fires for a throttled egress IP AND for a gate-shape mismatch. A 403
  burn-out from one machine is never by itself a shape verdict — first
  re-check the envelope (tools array present, session id generated by
  `zengate.SessionID()` and copied to all four headers), then suspect
  egress. `TestZenChatLive` (gated `OPENCODE_ZEN_LIVE=1`) burns one real
  call; it passed live from the Dev box with the new envelope on 2026-10-16
  (3.5s, "pong") along with the vision live test.
- History from pre-zen turns can contain assistant messages with `ToolCalls`
  and tool-role messages — the fold renders those as notes; nothing else
  needed. Session/memory files are preserved bit-identically.
- Symmetry notes: `internal/zengate/` holds the shared identity helpers
  (`SessionID`/`ProjectID`/`TraceHeaders`) and the embedded tools array
  (`zen-tools.json`); vision consumes the same package. NEVER send a random
  `ses_`-shaped id — the gate rejects ids that do not decode to a plausible
  timestamp (hard 403).

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
  the mainframe cookie vault
  (`%APPDATA%\mainframe\accounts\agent-browser\cookies\<fb-account-email>.cookies.json`,
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
- **RETIRED 2026-10-16: the Neon project** `lumen` / silent-math-00638479, org
  (org-rapid-base-02276796), aws-us-west-2, pg 17. Migrated to **Supabase
  Singapore** before Neon's ~2026-10-18 free-compute gate (see "Singapore
  migration" below); the DATABASE_URL no longer references Neon anywhere.
  Every DB this service touches (persistence snapshots, dedupe tables,
  whatsapp_sessions, pending_notifications, notification_deliveries) now lives
  in the single Supabase project. Never commit the DSN.

## Deploy: Render web service (2026-08-14)

- **PRIMARY service since 2026-10-16: `lumen-sg`** (srv-db8o8qgn74is73c7kk60),
  workspace "Bayazid's workspace" (workspace tea-cu1mom1u0jms738ka280), free
  plan, **singapore**. URL https://lumen-sg.onrender.com, health /api/health.
  The original Oregon service `lumen` (srv-d9vd3oh42hec738odeg0,
  https://lumen-aqyl.onrender.com) was **DELETED** on 2026-10-25 after
  Singapore ran stably for 9+ days. All deploys/env changes go to
  `lumen-sg`; examples below use its service id.
- Dockerfile multi-stage build (golang:1.25 → debian:bookworm-slim + ffmpeg),
  binds 7860, `serve -config /app/config/production.yaml`. Auto-deploy on main.
- **docs-only pushes must carry `[skip render]` in the commit message** (or
  `[render skip]` / `[skip deploy]`) - Render's webhook skips the whole push
  then, so an AGENTS/README edit does not restart production and drop the
  platform sessions. verified 2026-10-09: `de85957` was pushed with the marker
  and created NO deploy (deploy list still showed only the previous commit);
  the marker must be in the LAST commit of the push. the service lives under
  the **bayazid10@gmail.com** mainframe render profile (`mainframe\
  render-account.ps1 use bayazid10@gmail.com`); list deploys with
  `GET /v1/services/srv-db8o8qgn74is73c7kk60/deploys` - responses are a bare
  JSON array of `{deploy: {...}}`, so `foreach ($item in @($resp)) { $item.deploy }`,
  NOT `$resp.deploys`.
- **Render env changes only apply on the NEXT DEPLOY (2026-10-09)**:
  PUT/DELETE on `/v1/services/<id>/env-vars/<KEY>` updates the stored env, but
  an instance started by suspend/resume still boots with the OLD env (hit it:
  `WHATSAPP_PROXY_URL` deleted via API, suspend+resume, and the fresh boot log
  still said `WhatsApp proxy configured (E2EE only)`). After any env change,
  trigger `POST /v1/services/<id>/deploys` and wait for `live` before judging
  the change. Also: the env-vars API rejects `{"value":""}` (empty string) on
  PUT — DELETE the var instead of blanking it.
- `config/production.yaml` is now TRACKED in git (was gitignored) and is
  secret-free: `discord.bot_token_env: DISCORD_BOT_TOKEN` (added 2026-08-14,
  bot_token fallback if the env var is unset). Render env vars:
  DISCORD_BOT_TOKEN (the real token), LITELLM_API_KEY (HF token from the local mainframe hf profile,
  authorizing https://alchoholpad-litellm.hf.space). GitHub push protection
  (secret scan) blocks any commit containing the token — never commit it.
- First create via `render services create` did NOT auto-trigger a deploy
  (status stayed empty); fix: `POST /v1/services/<id>/deploys` via curl.
- Local Windows runs still use config/lumen.yaml (gitignored).

### Singapore migration (2026-10-16): Neon → Supabase SG, Oregon → Render SG

Full-stack move to Singapore (rule 31: closest region to Dhaka) + Neon's
free-compute gate (~2026-10-18). All free tier.

- **DB = Supabase project `pwtjjqvhgfgmxknnizjl`**, region `ap-southeast-1`,
  ACTIVE_HEALTHY. Data migrated by `pg_dump` from Neon (1,931,252 B, 8 tables
  in TOC) + `pg_restore --clean --if-exists --single-transaction --no-owner
  --no-privileges` (exit 0, 9s). Row counts verified EXACT on all 8 tables
  (app_state 2, crack_seen 55, game_seen 69, lumen_snapshots 55,
  notification_deliveries 109, pending_notifications 0, steam_seen 35,
  whatsapp_sessions 1) and `public` holds exactly those 8 tables.
- **DATABASE_URL final shape = session-mode pooler (IPv4)**:
  `postgresql://postgres.<ref>@aws-0-ap-southeast-1.pooler.supabase.com:5432/postgres?sslmode=require`
  (password in the Render env + the vault `.env (production)` item; NEVER
  commit it). User format is `postgres.<ref>`, NOT bare `postgres`.
- **GOTCHA — the direct host can never work from Render (root cause of the
  first `update_failed` deploy)**: `db.<ref>.supabase.co` is **IPv6-only**
  (AAAA `2406:da18:...`, ZERO A records) and Render free containers are
  `v6=false`, so a direct dial always dies with
  `dial tcp [2406:...]:5432: connect: network is unreachable`.
  `db-pool.<ref>.supabase.co` does NOT exist as a hostname. The pooler host
  (`aws-0-ap-southeast-1.pooler.supabase.com`) is IPv4-only and in-region —
  that is the only Render-safe DSN target. Never "fix" a dial error by
  repointing at the direct host.
- lumen is pooler-safe: no LISTEN, no advisory locks, no session-level SQL.
- **Local DB access**: psql runs through the mihomo SOCKS relay pattern
  (`neon.com\neon-via-proton\socks5-fwd.ps1`, listen 127.0.0.1:5435 → target
  pooler:5432, `hostaddr=127.0.0.1` so the relay dials; details/quirks in that
  folder's AGENTS.md). Local DNS cannot resolve `*.supabase.co` at all — use
  DoH for lookups; the relay path works because mihomo resolves remotely.
- **Re-pointed to `https://lumen-sg.onrender.com` same day** (commit
  `5caf385` `[skip render]`, deploy `dep-db8q3hjl550s73eq1i2g` → live):
  `production.yaml` `health_watch.whatsapp_pair_url` + `notify.webhook_url`,
  cron-job.org `8265305`, UptimeRobot `804112595`, automata-private
  `protonvpn.com\whatsapp-path-watchdog.ps1` (HealthUrl/NotifyUrl) +
  `BYPASS-DESIGN.md`, and `facebook.com\.env.local`
  (`MURMUR_HF_SPACE_URL`). Old Oregon service suspended as rollback insurance
  (see Deploy section above).
- **Vault (rule 30)**: the live pooler DSN is the `DATABASE_URL=` line in the
  vault item `github.com/FahadBinHussain/lumen-agent / .env (production)`
  (notes are KEY=value lines; restore with
  `bitwarden.com\env-sync.ps1 -Repo lumen-agent -Env prod`).
- The STATE RESET procedure (suspend → delete rows → resume) now targets this
  Supabase DB (via the relay above) instead of Neon.
- Neon project `lumen`/silent-math-00638479 is RETIRED — the old DATABASE_URL
  is gone from Render; never repoint anything at Neon.

### Lumen keepalive redundancy (2026-09-28)

Lumen is on Render's **Free** plan, so it must have independent external HTTP
keepalives. The canonical target is `https://lumen-sg.onrender.com/api/health`
(re-pointed 2026-10-16 from lumen-aqyl; not the local `127.0.0.1:8791` bridge
used by the Messenger cookie-health tool). Side effect worth keeping: the two
keepalives hitting lumen-sg every 5/10 min also stop the free instance's
15-min idle spin-down.

- **cron-job.org** account `fahadbinhussain001@gmail.com`: job `8265305`,
  title `render keep-alive - lumen`, GET every 10 minutes, currently enabled.
- **UptimeRobot** account `fahadbinhussain001@gmail.com`: monitor `804112595`,
  title `render keep-alive - lumen`, HTTP every 5 minutes, currently STARTED.
- Keep both providers configured for Lumen. The two-provider setup is deliberate:
  cron-job.org automatically disables jobs after more than 25 consecutive
  failures; it has no setting to disable that safety behavior. Keep
  `onFailure`, `onSuccess`, `onDisable`, and response saving enabled for the
  cron-job.org Lumen job so failures are observable.
- Do not mistake `lumen-cookie-health-watch` (a local, Messenger cookie-refresh
  watchdog) for the Render keepalive. Its `.env.local` must point at the
  deployed URL for cookie refresh, but it is not the WhatsApp or full-Lumen
  watchdog.
- When checking deployment health, verify both external monitors and Render's
  `/api/health`; a 200 only proves the web process is alive, not that the three
  platform sessions are connected.


## Merged platforms (2026-08-12): murmur Messenger + WhatsApp ports

This fork now runs all three platforms from one binary: the upstream Element Orion
Discord agent runtime, plus Messenger and WhatsApp channels ported from the murmur
repo (github.com/FahadBinHussain/murmur; the local clone
`C:\Users\Admin\Downloads\murmur` was deleted 2026-10-09 - clone it again if the
source is ever needed). **The murmur repo stays untouched** -
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
  Authorization: Bearer; no-op when bridge.secret unset). Live on Render —
  `messenger.enabled: true` in production.yaml and the watchdog's
  murmur-cookie-refresher.mjs hits this endpoint on every refresh.
- `GET /api/health` — always 200; body since 2026-10-04 is JSON
  `{"status":"ok","platforms":{"messenger":bool,"whatsapp":bool,"discord":bool},
  "vision":{...}}` (live `isPlatformConnected` flags) so external watchers can
  tell web-process alive from mouth connected. The local cookie-health watchdog
  gates its auto-refresh on `platforms.messenger`.
  The `vision` object (added 2026-10-06) reports the image-describer engine:
  `{enabled, status, model, base_url, calls, consecutive_failures, last_error,
  last_error_at, last_success_at}` with `status` one of `disabled` /
  `never_called` / `ok` / `degraded` / `misconfigured` — degraded means the
  last Describe call burned all retries, misconfigured means the describer can
  never work (bad proxy/base/model, or `vision.enabled` with no runner wired).
  Stats reset on every deploy (in-process, never persisted).

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
is stripped; the rest becomes the prompt for the agent runner.

Quote-reply context forwarding (2026-10-26): when an incoming message replies to
another message, the quoted text is captured from Meta (`ReplySnippet` or
`ReplyMessageText` on `table.LSInsertMessage`) and WhatsApp (`ctx.GetQuotedMessage()`),
and prepended to the prompt as `[Replying to: %q]\n%s` (truncated at 1500 chars).
This allows the model to immediately understand references like "what r ur thoughts on this?"
even for old messages or automated notifications sent before session history recording.

Web tools and search engine backends (2026-10-26): `api.duckduckgo.com` was
deprecated and blocked cloud datacenter IPs (like Render) with anti-bot CAPTCHAs,
and `http.DefaultClient` had no timeout, causing `search_web` to hang until
agent context cancellation. Fixed by: (1) using `webClient` (10s timeout) across
all web tools so nothing hangs unbounded; (2) rewriting `search_web` to check for
`BRAVE_SEARCH_API_KEY`/`TAVILY_API_KEY`, using HN Algolia + Wikipedia search
when no external key is set; (3) adding `fetch_web_page` tool to fetch public URLs,
extract title/meta description, and clean HTML into readable text so the agent can
read linked articles and repositories directly.
No murmur `/ai`
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
(messenger 2637078310061988 / whatsapp test JID (Render env) /
discord 1537650032441032765) — keep it that way unless a real thread needs
admin.

Notification toggle commands (added 2026-10-25): `/notify`, `/notify status`
(view active & muted automated feeds for this thread), `/notify off [feed]`,
`/notify on [feed]` (or `/mute [feed]`, `/unmute [feed]`). When executed in any
thread, the caller's thread is inferred automatically (no thread ID required).
Feeds: `free_games` (gamebot), `steam_updates`, `crackwatch`, `neon_usage`,
`supabase`, `all` (`*`). Targeting a foreign thread (`/notify off <feed> <id>`)
is restricted to `bridge.admin_threads`. Config baseline opt-outs live in
`bridge.disabled_notifications` (e.g. `messenger:953525124128433: ["gamebot"]`),
and runtime overrides persist to `<session_dir>/bridge-disabled-notifs.json`
(Neon/Supabase-snapshotted via the persist toucher). Muted notifications return
`status: "muted"` from `/api/automation/notifications`, mark dedupe keys
delivered in Neon, and are dropped from pending queue retries.

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

- **git push fails with `bash: /dev/tty: No such device or address` / `could not read
  Username for 'https://github.com'`** (hit 2026-10-06): this clone had a LOCAL
  `credential.helper=` (empty value) in `.git/config`, which overrides the scoop git
  system config and kills `git-credential-manager.exe`. fix:
  `git config --unset-all credential.helper` in the repo, then `git push -u origin <branch>`
  (the branch had no upstream yet either). other clones were unaffected - it is per-repo.

- **Messenger redelivery ghost replies (fixed 2026-08-15, commit 38dac85)**: Meta
  redelivers messages that were queued while the MQTT socket was down — they
  arrive again on reconnect with their ORIGINAL send timestamp. The murmur-style
  boot-time guard in `relay()` (skip msgs older than client start) does NOT catch
  them when the original send happened after boot. Symptom: the bot replies to an
  already-answered message hours later (user saw "who are u" answered 3x — 23:25,
  00:13, 00:31 local, each delivery ~3s after a socket drop). Fix: `relay()`
  dedups on `MessageID` (map with 24h retention, pruned on growth >500). Keep
  both guards — the time guard still protects against fresh-boot history delivery.
- **Messenger permanent-error auto-reconnect (added 2026-10-04)**: messagix
  `Event_PermanentError` used to be terminal — the client logged it and gave
  up forever, so the 2026-10-03 drop (edge RST → reconnect refused, `unknown
  connection code 24`) left messenger dead 17h while health-watch falsely
  reported "auto-retrying, will come back on its own". Now the event arms
  `armReconnect` (single-flight): full `ReloadCookies`-equivalent reload +
  relog 60s after the error, then every 5m until the socket is actually up;
  the 60s keepalive tick arms the same cycle if the socket dies with no
  permanent-error event, and it now starts once (`keepaliveOnce`) instead of
  leaking a ticker per Ready/reload. `ReloadCookies` is serialized with
  `reloadMu` so an external cookie upload and an auto-reconnect can't rebuild
  the client concurrently. Retry loop never gives up (transient refusals heal
  in ~1m; cookie rotation needs the external watchdog's fresh cookies, worst
  case one watchdog interval). Delays/`reloadFn` are injectable fields for
  the tests in `reconnect_test.go`.
- **Meta rejects messages containing literal `scrape.do` (found 2026-10-04)**:
  Facebook silently refuses a send whose text contains `scrape.do` (dailybnp's
  fetch-route error dumps include `scrape.do returned HTTP 401`) — the task
  executes with no transport error, but Meta answers with an error response
  (Render logs: `insertMessageSendErrorForMessage` data `[19,"1404006"]` +
  `Facebook server log: Write error response from server`). `SendText` gets
  no optimistic-replace entry → returns "" → the BNP worker acks
  `messenger send returned no message id` → infinite retry with the same
  rejection (one outbox row burned 103 attempts across 10-03/10-04).
  Bisected on the test thread: pipes, `->`, brackets, parens, `HTTP 401/403`,
  bare word `scrape`, URLs, and 2151-char messages all pass; ONLY the bare
  domain `scrape.do` triggers the reject (thread-independent). `SendText` now
  logs `send returned no message id (otid=..., replaces=...)` on this silent
  path instead of returning "". Fix belongs in the message PRODUCER
  (dailybnp must not emit bare `scrape.do` in message text — rewrite as
  `scrapedo`); do NOT sanitize silently on the send side. Diagnostic filter:
  `text=insertMessageSendError` on the Render logs API.
- **Meta send without optimistic replace table (fixed 2026-10-26)**:
  When Meta delivers messages with complex URLs or sync transactions (e.g. `LSExecuteFirstBlockForSyncTransactionV4`),
  `resp.LSReplaceOptimsiticMessage` may be omitted in the immediate RPC reply even though `ExecuteTasks` succeeded (`err == nil`)
  and the message is actually posted to the thread. If `SendText` returns `""` in this case, `bridge.SendMessage` returned
  `messenger send returned no message id`, which caused `drainPending` to believe the delivery failed and retry every 5 minutes,
  resulting in duplicate sends to the chat. Fixed by using a synthetic ID (`otid_<otid>`) on successful task completion when
  no replace table is present, and adding a 3-attempt ceiling in `drainPending` to prevent infinite loops.
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
    message ID was recorded (whatsmeow BuildEdit, `whatsapp.EditWindow` =
    20 min server window; mapping keyed "messengerID|jid" is KEPT across
    edits — never consumed — so repeated edit rounds [detected→published,
    then the translation refresh] all hit the same bubble; a mapping is only
    replaced when a fresh mirror had to be sent [past the window, edit
    failed, or no mapping recorded] and the fresh path is logged loudly);
    discord edits IN PLACE too via REST PATCH (mapping keyed
    "messengerID|channelID", `DiscordHealthClient.EditPlainText`, no edit
    window), same fresh-message fallback. the original bug: the whatsapp group kept stale
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
    Neon API keys) + `thread_id` + optional `state_path`. Wired on Render:
    steam_updates, free_games, neon_usage (and crack_watch below) are all
    `enabled: true` in production.yaml (code defaults stay false; local
    lumen.yaml keeps them off).
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
  - **Rate-limit handling (2026-09-07):** Render's shared egress IP gets
    intermittent HTTP 429s (~1 in 5 polls; the feed itself is fine — verified
    200 from residential). Old code burned 3 rapid retries into the 429, and
    429s looked identical to real failures. Now: 429 honors `Retry-After`
    (default 60s, capped 3m) with exactly ONE delayed retry, logged loudly as
    `crackwatch: rate-limited by reddit ... backing off`; other 4xx fail fast
    with no retries; transport/5xx keep the 3x backoff. Covered by
    `crackwatch_test.go` (httptest 429→200 backoff timing, 404 fail-fast).
  - **tech_news poller (added 2026-10-25)**: `internal/notify/technews.go`
    polls multi-source tech feeds (default: Ars Technica, The Verge,
    BleepingComputer, Hacker News, or custom `feeds` list in YAML). Dedupes
    against Neon `public.tech_seen` (guid, source, title). On first boot for a
    feed, seeds existing items into `tech_seen` to avoid blasting historical
    articles into chats. Pushes to webhook as source `tech-news` with
    dedupeKey = article GUID/URL. Config: `notify.tech_news.{enabled,interval,
    thread_ids,webhook_url,feeds}`. Controllable via `/notify off tech_news` and
    `/mute tech`.
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
    (github fine-grained PAT scoped to lumen-agent only, see local mainframe github profile,
    contents:write). Dockerfile now installs
    `postgresql-client git openssl` (pg_dump + git needed). code:
    `internal/notify/neonexport.go` (`exportNeonOrg` per over-threshold org,
    `pushExports` batched single commit). Recovery of an encrypted export: the key
    lives in the vault item `github.com/FahadBinHussain/lumen-agent / .env (production)`
    and restores with `automata-private\bitwarden.com\env-sync.ps1 -Repo lumen-agent -Env prod`
    (writes `lumen-agent\.env.local`, gitignored; NOTE env-sync's default `-Env` is
    `dev` - a `(production)` item is skipped silently without `-Env prod`). the old
    standalone `C:\Users\Admin\Downloads\lumen-export-key.txt` copy (2026-08-24,
    predates the vault env convention) was migrated into that vault item and DELETED
    2026-10-14; the same key also lives on Render as env `LUMEN_EXPORT_KEY`. File names are
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
    same account resets the bucket. storage reads `peak_data_storage`/
    `data_storage` with a v3_metrics fallback (2026-10-02 note below).
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
- **messagix V4 sync migration (2026-10-08)**: Meta switched lightspeed sync
  blocks to `executeFirstBlockForSyncTransactionV4` (V1 + a `CurrentSeqID`
  field inserted at index 4). The pinned June mautrix-meta didn't know the op
  (`Unknown dependency in sp` -> `Skipping dependency with no reference` ->
  `No transactions found`) so the connect-time sync's `HandleEvent` never
  fired: entire sync tables (including queued inbound `upsertMessage` rows)
  were silently dropped, cursors stayed `last_applied_cursor: null` forever,
  and inbound messages died for weeks while typing indicators (unchanged op)
  kept flowing. Fixed by bumping to mautrix-meta **v0.2609.0** (+ go 1.26.0,
  Dockerfile `golang:1.26`, and a local `replace github.com/imroc/req/v3 =>
  github.com/beeper/req/v3@...` because a dependency's replace does not
  propagate). Event API changed with the bump - handler cases are now
  `ConnectedEvent` (was `Event_Ready`, no `ConnectionCode` field),
  `*table.LSTable` (was `Event_PublishResponse`; call
  `client.PostHandlePublishResponse(tbl)` after processing to advance
  cursors), `TransientDisconnectEvent`, `PermanentErrorEvent`,
  `ReconnectedEvent`; an unknown event type is logged LOUDLY, never dropped.
  Also: the Render log API truncates messages at exactly 100000 chars and
  zerolog puts `message` last, so `text=` searches miss the message key on
  huge responses - prefer timestamp-windowed raw pulls when a log line "isn't
  there". No runtime patching of the June structs is possible: field indexes
  shift after V4's insert, so mapping V4 onto V1 misaligns cursors silently.
- **Messenger replies were lost to a SELF-DEADLOCK, not Meta (found + fixed
  2026-10-09, third try)**: symptom — model replies (logs show `assistant ...`)
  but the user only ever sees "thinking."; Render logs show
  `dgw: oneoffstream: data receive timed out` then
  `use of closed network connection` then `gave up after N attempts`.
  Root cause (mautrix-meta v0.2609): every `*table.LSTable` event is delivered
  on ONE frame-handler goroutine (dgw readLoop → `incoming` chan, cap 64 →
  `syncManager.HandleEvent` → our handler). Our handler ran the whole agent
  run inline, so the run's own `SendText` → `DoOneOffStream` waited for a
  **data frame that only the goroutine we were blocking could deliver** →
  structural 5s timeout at attempt 1. Worse, `ForceReconnect()`'s `CloseNow`
  fires `fatalError` but `readLoop`'s `wg.Wait()` still waits for the stuck
  frame-handler goroutine, so `Connect` never returns, `canSendMessages` never
  clears, `WaitUntilCanSendMessages` returns instantly against the stale flag,
  and every retry writes to the dead conn — all 5 attempts burn in ~4s. Proof:
  the 02:29:29→02:29:51 log blackout (nothing processed while the handler ran)
  with `socket error (attempts 1)` + reconnect landing the moment the handler
  returned. "Meta acked but sent no data" was never Meta — Meta's data frame
  was queued behind our own handler. Attempts 1 and 2 (blind 1/3/5s backoff,
  then ForceReconnect + readiness wait) could never work against a stuck
  dispatcher. **The fix**: `relay()` no longer calls the handler inline —
  `enqueueDispatch` parks it (with `context.WithoutCancel`, same trap as the
  cookie-upload bug) on a single ordered dispatcher goroutine
  (`dispatchLoop`/`runDispatched`, started by `startDispatcher`, panic-recovered
  so a handler panic can't silently strand the queue); cursors still advance
  inline via `PostHandlePublishResponse`. Keep dispatch SERIAL and in arrival
  order — the bridge clones session history per run, so concurrent handler
  runs on one thread would race history. Keep `executeRetry`/`retryLSTable`
  (ForceReconnect + `WaitUntilCanSendMessages(15s)` + 1s grace, 5 attempts,
  backoffs 2/5/10/15s only on readiness timeout) as the safety net for REAL
  transport failures from any goroutine (BNP, health-watch, dispatcher) — with
  async dispatch attempt 1 should just succeed; if
  `data receive timed out` ever appears again, that IS a genuine Meta stall
  and is now recoverable. Same task closure across retries so the otid stays
  stable (Meta dedupes landed-but-timed-out sends) — never regenerate the otid
  per attempt. Covered by internal/messenger/retry_test.go (schedule/give-up/
  cancel) and dispatch_test.go (off-goroutine dispatch, arrival order, panic
  survival, ctx outliving the event).
- The full test suite (`go test ./...`) is green on Windows (verified
  2026-10-05) — there are NO pre-existing failures; any red test is a real
  regression, never wave it through as "pre-existing".
- Messagix needs a full cookie set (`cookies.GetMissing` fails startup otherwise);
  refresh via `/api/cookies/upload` (murmur-cookie-refresher.mjs) without restart.
  Since 2026-10-06 the refresher reads cookies LIVE from the running real Edge via
  the browser-use skill (CDP `Network.getCookies` on `MURMUR_REFRESH_FB_URL`,
  default `https://www.messenger.com`) - no cookie vault, no saved snapshot, no
  per-account env. Point it at the local bridge with
  `MURMUR_HF_SPACE_URL=http://127.0.0.1:8791` and `HF_EMAIL` for the token.
- whatsmeow: first run has no device — QR is logged (`events.QR`) for phone scan;
  after linking, the device store file (`whatsmeow.db` under `whatsapp.store_dir`)
  is backed up to Neon so re-pairs survive machine moves.
- Old EO instance on this machine (PID 10772, since 2026-08-11) ran the
  pre-merge binary; replaced 2026-08-12 by the merged build (serve with
  config/lumen.yaml, bridge on 127.0.0.1:8791). messenger + whatsapp were
  unprovisioned back then; both are live on Render now.

## Test threads (2026-08-14)

Hardcoded test targets — use these for cutover test sends only:

- Messenger thread `2637078310061988` (already in `messenger.allowed_thread_ids`)
- Discord channel `1537650032441032765` (AlgoJect — where @murmur hello tests happen)
- WhatsApp test contact (JID in Render env `WHATSAPP_ALLOWED_JIDS`) (now gated
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
- **Cookie-refresh heal path was broken two ways (found + fixed 2026-09-07,
  messenger had been down since ~09-06 with the watchdog detecting but never
  healing):** (1) the refresher (murmur repo, DO NOT EDIT) needs
  `HF_EMAIL` + `AGENT_BROWSER_EMAIL` + `MURMUR_HF_SPACE_URL` in ITS OWN env —
  cookie-health.ps1 only forwarded the URL, and `AGENT_BROWSER_EMAIL` was set
  nowhere in the pipeline; `.env.local` now carries all three and the script
  logs env readiness before spawning (lengths only). (2) the refresher reads
  its Bearer token from `mainframe/accounts/hf/<HF_EMAIL>/token` — that file
  did not exist (`stored_tokens` there is a placeholder, NOT the token); it
  must equal the Render `ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET` value
  (recreated from Render on 2026-09-07). (3) **`handleCookieUpload`
  context bug (commit TBD):** it passed `r.Context()` into
  `ReloadCookies` → `Start` → background `Connect`, so every successful upload
  context-canceled its own MQTT handshake the moment the handler returned
  (swallowed as `context.Canceled`, zero logs) — refresh could NEVER
  reconnect; only a reboot brought messenger back. Now
  `context.WithoutCancel(r.Context())`. Symptom of all three combined:
  `foreground keepalive failed: not connected` every minute + `failed to send
  reply: not connected` on every send, with no socket-error lines ever.
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
- **Messenger account**: live on Render — `messenger.enabled: true` since
  2026-08, dedicated FB account with cookies refreshed from the mainframe
  vault (murmur's account is not used). **bridge.secret SET (2026-08-14)**:
  `ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET` on Render = the HF profile token
  from the local mainframe hf profile (the one the refresher sends as Bearer,
  verified by sha256). The Vercel poller project (murmur, prj_EWeinTGTbfW5iC2bciQ65ZuA4WyZ)
  had an old `HF_TOKEN` from an unrelated account that matches nothing in mainframe
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
whatsapp test JID (Render env), discord → both of those, and whatsapp/messenger
deaths also copy to the discord channel (`discord_channel_id`, currently
1537650032441032765 in AlgoJect) when set. So the mesh is symmetric: each
platform's alerts land on the other two. Discord connection state is tracked
in-process (`setConnected` via discordgo Ready/Resumed/Disconnect handlers +
`IsConnected()` on the discordbot Service, wired into the bridge with
`SetDiscord`), so a discord gateway drop fires the same dead/alive alerts
(discordgo auto-reconnects, so dead only lasts until the gateway is back).
**Discord handlers were dead code (found + fixed 2026-09-07, commit TBD):**
the Ready/Resumed/Disconnect closures were registered as single-arg funcs,
which discordgo rejects at runtime (`[DG0] AddHandler() Invalid handler
type, will never be called` ×3 at every boot — visible in Render logs since
the SetDiscord wiring). Effect: `IsConnected()` permanently false, so
discord-dead could never arm AND every discord-bound notification was queued
to `pending_notifications` instead of sent (the drain also gates on the flag,
so discord rows sat forever). The gateway itself was fine the whole time —
only the flag was broken. Handlers must be
`func(*discordgo.Session, *discordgo.Ready)` etc. Oracle after deploys: the
three `[DG0] Invalid handler type` lines must be GONE.
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
- **WhatsApp dead diagnosis (2026-09-03)**: `is dead` now classifies the
  cause via `healthwatch.go:diagnoseWhatsAppDead` — probes `127.0.0.1:1055`
  socks (`tailscaled` liveness), `tailscale status --json` (`BackendState`,
  `ExitNodeStatus.Online`, peer `100.76.10.50` online), and SOCKS egress to
  `1.1.1.1:443`/`web.whatsapp.com:443` (`golang.org/x/net/proxy`). Messages:
  `tailscale socks down`, `tailscale not running`, `no exit node`, `exit node
  offline — laptop-main ... (VPN/sleep?)`, `tail exit probe failed — proxy up
  but egress blocked (VPN filtering)`, `tailscale ok — whatsapp websocket
  down`. The VPN case you hit (Proton on laptop-main kills the exit) now
  surfaces as `exit node offline` / `tail exit probe failed` instead of the
  generic `auto-retrying` — no local watcher needed. Diagnosis runs only on
  the `dead_after`→`min_notify` firing path and logs
  `health-watch: whatsapp dead diagnosis: ...`.

Platform state plumbing: `messenger.Client.IsConnected()` added (messagix has no
disconnect event — Ready/Reconnected set connected=true, Event_SocketError /
Event_PermanentError set false; ReloadCookies resets it at the start).
`whatsapp.WhatsmeowClient.IsConnected()` is now mutex-guarded (was a racy plain
bool). Both are polled, not event-driven.

## WhatsApp pairing gotchas (2026-08-17)

- **RESOLVED 2026-08-17**: paired via PairPhone code flow (account via
  `WHATSAPP_PAIR_PHONE` env) and transferred to Render through Neon
  (`POST /api/whatsapp/session/upload`); QR page reports `"status":"paired"`.
  Commit `c9d4ae8` carries the fixes. The old test number (redacted) stays
  hard-blocked — never retry it.
- **Pairing identity fixes (the actual root cause)**: (1) whatsmeow `Client.QRClientType`
  was unset → QR advertised client type `9` (OtherWebClient) and the server
  rejected phone confirms ("unnamed device" + "couldn't link device"). Must be
  `PairClientChrome`. (2) `PairPhone` display name must be `Browser (OS)`
  format (`"Chrome (Windows)"`); `"lumen"` → server returns `400 bad-request`
  on the code flow. Set both in `internal/whatsapp/whatsmeow_client.go`.
- **2026-09-03: QR never works as of now, phone-code linking works** — the
  `GET /api/whatsapp/qr?format=png` / `?format=html` QR rotates but phone scan
  always fails (server returns 401/logged-out immediately after scan); use
  phone-number pairing instead: `POST /api/whatsapp/pair` with empty body
  (server uses `WHATSAPP_PAIR_PHONE` / `WHATSAPP_PHONE` env) → code → phone
  Settings → Linked devices → Link with phone number.
- QR page no longer hardcodes a phone number (removed for privacy,
  `notifications.go` page uses env var); pairing endpoint accepts empty body
  and falls back to `WHATSAPP_PAIR_PHONE` env.
- **2026-10-09: pairing-readiness signal = QR page state, NOT health** —
  whatsmeow dispatches `events.Connected` only from `handleConnectSuccess`
  (post-auth), so `health.platforms.whatsapp` (`IsConnected()` flag) stays
  `false` the whole time a QR/code pairing session is up and only flips true
  after pairing succeeds — waiting for `platforms.whatsapp: true` before
  clicking "Generate linking code" is circular and never fires. The real
  socket-up signal while unpaired: `GET /api/whatsapp/qr` returns
  `"status":"qr"` / the page shows "QR live" (the wrapper clears QRCode() on
  `Disconnected`, so a live QR ⇔ socket up ⇔ `PairPhone` works).
  `"status":"waiting"` = egress gap, page retries every 4s. Same day: QR page
  redesigned (`ea08d7c`) — box starts hidden, all states render as colored
  text (qr green / waiting yellow / error+unauthorized red), failed PNG load
  falls back to placeholder, so tail-exit flaps no longer show a
  broken-image icon.
- **QR PNG 401 (2026-10-09, `d5f3a2c`)**: the page built the image URL as a
  query-only relative `'?format=png&t=...'`, which REPLACES the page's whole
  query string and drops `token` → `whatsappPairAuthorized` 401s → `<img>`
  onerror forever: status says "QR live" (the JSON poll always carried the
  token) while the box shows "QR image failed to load". Fix: the png URL must
  be `'&token='+encodeURIComponent(token)`-suffixed — pinned by
  `TestWhatsAppQRPageNeverShowsBrokenImage`.
- **`"status":"paired"` reads the STORE FILE, not server truth (observed
  2026-10-09)**: unpaired runtime states never persist (`sqlstore.NewDevice`:
  "No data is actually stored before Save is called"; only the pairing
  process auto-saves), so `whatsmeow.db` + the Neon `whatsapp_sessions` row
  keep holding whatever was last PAIRED — including a session the server
  already rejected (02:53 `401 logged out from another device`). Symptom:
  `qr?format=json` = `paired` while `health.platforms.whatsapp` = false and
  every dial fails. Treat as "stale session awaiting server validation": the
  first successful dial either authenticates (valid — health flips true) or
  401s → whatsmeow deletes the store → fresh device → QR appears on the fixed
  page → pair immediately inside that egress window. The bridge.Run 20s
  initial-dial loop + scheduleReconnect 15s loop both retry forever, so no
  intervention is needed until one of those two outcomes logs.
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
- **WhatsApp egress: DIRECT from Render since 2026-10-09 — the tailscale
  exit-node chain below is RETIRED for whatsapp**. The old premise "Render
  datacenter IPs get TLS-blocked by WhatsApp" (2026-08-30) was diagnosed
  while `tls_client.go` was still broken (the uTLS bugs in the pairing
  gotchas above) — it was never a real IP block. With the fixed uTLS client
  the DC dials fine: verified 2026-10-09 (fresh QR session came up direct,
  `POST /api/whatsapp/pair` produced a code, zero laptop/exit-node/socks in
  the path). `WHATSAPP_PROXY_URL` was DELETED from Render that day; do not
  re-add a proxy unless a NEW direct-dial failure is captured in logs from
  the fixed client (`tcp dial:` prefix = our dialer). What it replaced
  (historical, 2026-08-30..2026-10-09):
  before that whatsapp egressed via a local
  socks5-proxy + sockschain sidecar, replaced 2026-08-30 by
  a NATIVE tailscale EXIT NODE: laptop-main advertises
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
  ACTIVE laptop-side heal (mainframe cookie-vault push); WhatsApp is
  passive-wait, so no watch. bridge `/api/health` returned bare "ok" until
  2026-10-04 — it now returns JSON with the three platform `IsConnected`
  flags (messenger/whatsapp/discord), which is also what the cookie-health
  watchdog reads to detect a dead messenger (the 2026-10-04 17h outage read
  healthy on every prior check). `TS_AUTHKEY` = a dedicated EPHEMERAL reusable authkey (made 2026-10-15 in the
  admin console: Reusable ON, Ephemeral ON, Tags OFF - key id
  `kKZqd89Jii11CNTRL`, expires 2027-01-13). Kept in the Render env + the
  Bitwarden `github.com/FahadBinHussain/lumen-agent / .env (production)` item,
  NOT in `console.tailscale.com` (that item holds the fleet key, and its reader
  returns the FIRST `tskey-auth-` in the notes - a second key there would make
  new-machine provisioning join with the wrong key). **CORRECTION 2026-10-15: a
  real ephemeral auth key is the only mechanism that gets container nodes
  auto-removed** - under the old fleet key the mem: flag got no server-side
  `Ephemeral` mark (not badged in the admin console, so never scheduled for
  removal; see the mem: block below). **VERDICT 2026-10-15 (behavioral):** the first node under the new key (`lumen-render-2`, offline 13:13:27 UTC) auto-removed 65-68 min later - still present at 14:18:27 UTC, gone by 14:21:28 UTC - while the fleet-key ghosts stayed listed; self-reap verified. Rotation: console >
  Settings > Keys > Generate auth key (Reusable ON, Ephemeral ON, Tags OFF) ->
  `PUT /v1/services/<id>/env-vars/TS_AUTHKEY` -> redeploy (manual deploy POST;
  autodeploy on push does NOT fire). History: before 2026-10-15 this held the
  fleet key `kuQ9aboTv211CNTRL` (ephemeral:false); the original fleet key once
  expired and every fresh boot failed with `invalid key: API key
  k2bx6Qw2KB11CNTRL not valid` -> `Exited with status 1` -> deploy
  update_failed/crash loop while the old instance kept serving; GOTCHA:
  `tailscale up` failing = exit 1 via `set -e` in entrypoint.sh; a bad or
  expired key parks the deploy in update_failed until PUT with a valid key. **WhatsApp health distinction (2026-09-03)**:
  health-watch now differentiates `is dead (auto-retrying...)` vs
  `is logged out (needs phone re-pair via QR - not auto-recovering)` — logged-out
  is fatal (401 from WhatsApp, store deleted, `whatsapp_is logged out` requires
  QR at `GET /api/whatsapp/qr`). Logged-out arms immediately on fresh boots
  (transient dead still needs one prior alive), and `whatsmeow_client` now
  auto-creates a fresh device on `LoggedOut` (previously `Connect` failed with
  `invalid use of deleted device` and never produced a QR; now `NewWhatsmeowClient`
  skips deleted devices and `Connect` recreates the client). `LoggedOut` now
  also triggers `scheduleReconnect` (15s) like `Disconnected`. `--state=mem:` (COLON required) selects the
  in-memory state store and DOES set `LoginFlags = LoginEphemeral` on the
  CLIENT when the state path has the `mem:` prefix
  (`cmd/tailscaled/tailscaled.go`; the mem store is registered as `"mem:"` in
  `ipn/store/stores.go`, help: "use 'mem:' to not store state and register as an
  ephemeral node"). **GOTCHA fixed 2026-10-15:** the flag used to be bare
  `--state=mem` - matches no store provider prefix, so it silently fell through
  to a FileStore writing a file literally named `mem` and registered a NORMAL
  non-ephemeral node: every container boot left a ghost device that was never
  auto-removed (46 accumulated: `lumen-render-1..42` + a plain `lumen-render`;
  pruned to 4 on 2026-10-15). **CORRECTION 2026-10-15 - the colon alone is NOT
  enough:** with `--state=mem:` restored the client really did register
  ephemeral (tailscaled log `control: client.Login(6)` = `LoginInteractive|
  LoginEphemeral`; `direct.go` sets `Ephemeral: true` in the RegisterRequest),
  yet the control server never badged that node (`lumen-render-1`, fleet key -
  no `Ephemeral` mark in the admin console) and unbadged nodes are never
  scheduled for removal. **Only an EPHEMERAL AUTH KEY produces the server-side
  mark** (see the TS_AUTHKEY block above): nodes under the new key
  (`lumen-render-2`/`-3`) carry the `Ephemeral` badge. Console check: the
  machines page shows the badge; the v2 API exposes no device-level isEphemeral
  field (key-level only: `capabilities.devices.create.ephemeral`). entrypoint.sh
  runs tailscaled `--tun=userspace-networking
  --socks5-server=127.0.0.1:1055 --state=mem:` then `tailscale up
  --authkey=... --hostname=lumen-render --accept-dns=false
  --exit-node=100.76.10.50 --timeout=40s`. GOTCHA: `--accept-dns` is a
  `tailscale up` flag, NOT a tailscaled flag — passing it to tailscaled makes
  it print usage and exit 1 (deploy update_failed; fixed 2026-08-17, commit
  2a0ac08). **Initial dial retries forever (2026-09-07):** a single failed
  `Connect` at boot (tailscaled SOCKS `general failure` while the
  re-advertised exit was still settling) used to kill the whatsapp goroutine
  silently — whatsapp stayed dead until the next deploy, since post-connect
  auto-reconnect never engages when the INITIAL dial fails. `bridge.Run` now
  retries the dial every 20s until it succeeds. Verify: logs `entrypoint:
  tailscale userspace node up (<ip>), exit node 100.76.10.50` + `WhatsApp
  connected`; tailnet shows `lumen-render` (linux, active). **Runtime logout
  reconnect gave up after one failure (2026-10-09):** a *runtime* `LoggedOut`
  (server 401 "logged out from another device" at 02:53:17) created the fresh
  device fine, but `scheduleReconnect`'s single `Connect` attempt died 25s
  later on a transient exit-node SOCKS blip (`general SOCKS server failure`)
  and the chain just STOPPED — no further attempt ever fired, no QR event
  could arrive, pair page stuck on `{"status":"waiting"}` forever (distinct
  from the 09-07 boot-dial bug: that one is `bridge.Run`, this one is the
  post-logout path). Fix: the reconnect loop retries every 15s
  (`reconnectRetryDelay`, injectable `connectFn`) until a connect succeeds,
  logging the exact error on every attempt. Verify: logs repeat `whatsapp:
  not logged in, reconnecting for a fresh QR session` + `whatsapp reconnect
  failed — retrying` every 15s while egress is down, QR/pair session comes up
  when it heals.
- **Exit advertisement can silently vanish (2026-09-07):** laptop-main lost
  `AdvertiseRoutes` (prefs showed null; `exit-node list` offered only the
  offline desktop-main) — cause unknown (client update? VPN toggle?), effect
  was whatsapp `websocket not connected` on every send while the container
  looked healthy. Fix: `tailscale set --advertise-exit-node=true` on the
  laptop; admin-console route approval PERSISTS per device, no re-approval
  needed (verified via API: advertised + enabled `0.0.0.0/0, ::/0`). No
  container restart needed — tailscaled re-picks the exit and whatsmeow
  reconnects on its own. Probe for this class of outage without touching the
  container: `POST /api/automation/notifications` with a platform/thread —
  response `sent` vs `pending` reads the EXACT `IsConnected` flags the
  health-watch uses (a `pending` on all three = total mouth outage).
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
  endpoint ID since SNI is lost: `?options=endpoint%3Dep-green-lab-aroopq6v`.
  Neon project `lumen` = `silent-math-00638479`, branch `br-raspy-bar-ar7024bl`,
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
  Neon-under-Proton relay), and trigger a fresh deploy; **but see the 2026-10-14
  SOUL consolidation bullet: the running container's sync clobbers a row upsert
  ~1/min, so the safe order is suspend -> upsert -> resume, not just deploy**;
  verify via logs
  `persist: restored N file(s) from snapshot` + the agent prompt no longer
  saying "Element Orion".
- **SOUL consolidation (2026-10-08)**: three divergent Kite SOULs existed — the local
  `config/SOUL.md` (2073 B variant), the production `@workspace/SOUL.md` row (a 584 B
  trimmed variant nobody had on disk), and a profile-style draft (`Kite-SOUL.md` in
  Downloads). The profile-style draft won: its bytes were upserted onto the row
  (5471 B, sha256 deb2ff2d…) + fresh deploy, and `config/SOUL.md` was DELETED.
  **The Neon `@workspace/SOUL.md` row is the single source of truth; no local SOUL copy
  exists anymore.** old 584 B soul backed up at `C:\tmp\production-SOUL-backup-20261008.md`
  (scratch, never in the repo). gotcha learned: the row and the local file had silently
  drifted for a week because nothing ever asserted they matched — the new soul is
  second-person instructions in a profile-shaped file (it still starts with a markdown
  header, loads fine); the old "bootstrap ritual is complete, never mention it" line was
  missing from the first cut and was re-added same day as a Rules/Boundaries bullet -
  current production = 5546 B, sha256 55908d0c…, pushed 2026-10-14 03:01:48 UTC. to change the soul again (procedure proven 2026-10-14): edit the
  source file, then **suspend -> upsert -> resume, NOT upsert -> deploy** - a plain row
  upsert while the service runs gets REVERTED by the running container's own sync
  (local dir is authoritative while running; observed: row was overwritten back to the
  old 584 B bytes at 02:18:57, ~1 min after the first upsert, and the deploy that
  followed then restored the OLD file). exact order: `POST
  /v1/services/<id>/suspend` -> poll `/api/health` until it fails (proves the clobberer
  is dead) -> upsert with exact file bytes + sha (SQL-over-443 works under Proton, no
  5432 relay needed) -> `POST .../resume` -> health returns in ~100s -> re-read the row
  after ~100s and assert `updated_at` still equals the upsert time (a moved timestamp =
  something re-clobbered it). never rely on deploy alone for identity rows.
- Local pairing path: kill local instance AFTER pairing before deploying Render
  (same identity dual-connect conflict); transfer session via
  `POST /api/whatsapp/session/upload` (bridge secret auth) → Neon restore on boot.
- **uTLS direct dial was broken TWO ways (found + fixed 2026-10-09)** in
  `internal/whatsapp/tls_client.go`: (1) `DialTLSContext` returned
  `uconn.NetConn()` — the raw TCP conn under a LIVE TLS session — so the
  transport wrote plaintext HTTP into TLS and every response read back as
  `malformed HTTP response "\x17\x03\x03..."`; (2) Chrome's hello spec
  hardcodes ALPN `["h2","http/1.1"]` and its `writeToUConn` OVERWRITES
  `config.NextProtos`, while Go's http.Transport only handshakes/records ALPN
  state for a real `*tls.Conn` — so the server picked h2 and the transport
  read `\x00\x00...` as the status line. Fix: `BuildHandshakeState()` first
  (Extensions are LAZY — empty right after `UClient`), mutate the
  `*utls.ALPNExtension.AlpnProtocols` to `["http/1.1"]`, then `Handshake()`,
  then `return uconn, nil` (the uTLS conn itself). Real-dial probe:
  `LUMEN_NET_PROBE=1 go test -run TestChromeHTTPClientDirectDial
  ./internal/whatsapp/` (gated so plain `go test ./...` stays offline-green).
- **`SetProxyAddress` silently replaced the uTLS transport (found + fixed
  2026-10-09)**: `client.SetProxyAddress(addr)` with no options sets the
  `Transport` FIELD of `preLoginHTTP`/`websocketHTTP`/`mediaHTTP`, and
  `whatsmeow_client.go` had all three pointing at ONE shared
  `NewChromeHTTPClient` instance — so with `WHATSAPP_PROXY_URL` set (always,
  on Render) the field being replaced WAS our uTLS transport: prod never ran
  the Chrome fingerprint at all (plain Go TLS over socks; the "Chrome TLS
  fingerprint impersonation enabled" log line was lying, and prod's errors
  showed plain `socks connect tcp` with no `tcp dial:` prefix). whatsmeow's
  `setTransport` mutates fields, so even `SetProxyOptions{NoWebsocket: true}`
  would gut a shared instance through `mediaHTTP`. Fix: media gets ITS OWN
  `NewChromeHTTPClient` instance + `SetProxyAddress(addr,
  SetProxyOptions{NoWebsocket: true})` (media-only proxy = what the "E2EE
  only" log line always claimed); websocket/prelogin keep the uTLS client,
  which dials the proxy itself. Keep the instances separate if you touch this.
- **Logout-alert pair link used to dead-end (found + fixed 2026-10-09)**:
  `handleWhatsAppQR` ran the state checks BEFORE the `format=html` branch, so
  the browser that opened the alert's link got a raw `{"status":"waiting"}`
  JSON blob instead of the page whenever the socket was down at click time
  (the normal case right after a logout) — and even on the page, the code
  needed a button click while a stale store said "paired - nothing to scan".
  Fixes: the html branch returns the page shell FIRST in every state (it
  polls the JSON itself); `stale` status (store says paired + socket down)
  warns and keeps polling instead of claiming paired; the page auto-generates
  the linking code while `status=qr` (retry each 4s tick until one shows) and
  hides the code on any non-qr state and on ref rotation (a code dies with
  its session). Pinned by `TestWhatsAppPairPageAlwaysServesHTML` +
  `TestWhatsAppPairPageAutoShowsCode`.

## Neon warning delivery (2026-09-23)

- Deduped poller notifications are persisted to Neon before delivery and
  remain queued indefinitely until the platform send succeeds. This is
  intentional at-least-once delivery: never add age-based expiry to Neon
  warnings, and keep the same `dedupeKey` so retries cannot create duplicate
  queue rows.

## WhatsApp phone pairing (2026-09-23)

- The public pairing page must collect the phone number and send it as
  `{phone: ...}`; posting `{}` is not enough unless a server-side phone env
  var exists. Pairing errors are returned as JSON so the page can display the
  actual backend error instead of failing with a JSON parse exception. Enter
  the number with its international country code and no leading trunk zero.

- Pairing access is private: Render must define `WHATSAPP_PAIR_TOKEN`. The
  QR page and pair API require that token, and pairing always uses the saved
  `WHATSAPP_PAIR_PHONE`/`WHATSAPP_PHONE`; never restore a public pairing URL
  or accept arbitrary request-body phone overrides.

## Neon notification dedupe (2026-09-23)

- Successful deduped notifications are recorded in Neon
  `notification_deliveries`, not only in local Render state. This prevents a
  restart or redeploy from sending the same quota-period warning again.
- Pending rows store the already-rendered notification text with an empty
  title; otherwise the queue drain prepends the title a second time.
- **GOTCHA (fixed 2026-10-08): dedupe keys are per-DESTINATION, never bare
  per-item.** A poller loops its `thread_ids` and POSTs the same `dedupeKey`
  once per thread (free-games = `30738305889116993,953525124128433`), and a
  bare key let the FIRST thread's delivery dedupe every later thread out of
  both gates: `IsDelivered(dedupe_key)` in the handler AND the
  `pending_notifications` unique index (`ON CONFLICT (dedupe_key) DO NOTHING`)
  — so the 2nd free-games group got nothing from the day this dedupe shipped
  (2026-09-23) until `scopedDedupeKey()` (`internal/bridge/notifications.go`)
  rewrote the key to `<key>|<platform>:<threadID>` (route posts:
  `<key>|route:<name>`) before any dedupe read. Symptom of a regression here:
  one subscribed thread receives feed posts, the other(s) are silent while
  every poller/log looks green (the skipped POST answers `deduped: true`).
  Keep the scoping if you touch the handler; same-thread retries must stay
  stable (`TestScopedDedupeKeyIsPerDestination`).

## CrackWatch dedupe (2026-09-23)

- CrackWatch can publish the same release under multiple Reddit post URLs.
  The poller dedupes normalized release titles as well as Reddit GUIDs, while
  still recording duplicate GUIDs so they are not reconsidered every poll.

## Messenger long messages (2026-09-23)

- Messenger notifications are split below the platform limit using Unicode
  rune counts, not byte counts. Each continuation is labeled `[part n/total]`
  so Bengali and other multibyte text cannot be cut mid-character or appear
  silently truncated.
- Edited notifications use the same behavior: edit the first chunk in place
  and send continuation chunks as new messages.
- Keep the Messenger chunk ceiling at 900 UTF-8 bytes; Bengali characters are
  multibyte and the edit endpoint has an observed effective limit near 1000
  bytes despite the larger normal-send limit.

## WhatsApp long messages (2026-09-23)

- WhatsApp notifications use Unicode-safe labeled splitting with a
  conservative 900-byte chunk size. The WhatsApp API accepts larger
  messages, but the bridge/client path has an observed effective limit near
  1000 characters, so the lower ceiling avoids silent truncation.
- WhatsApp edited notifications also edit the first chunk and send any
  continuation chunks separately.

## BNP whatsapp mirror: every edit round edits the SAME bubble (fixed 2026-10-19)

Live bug: one bssnews article produced TWO whatsapp bubbles at the same
minute ("detected · bssnews news" + "published on dailybnp · bssnews news")
in the BNP group. Root cause was in `bridge.EditRoute`'s mirror mapping, not
the dailybnp outbox: `waEdits` was keyed `"messengerID|jid"` and CONSUMED on
the first in-place edit, so the old detected→published contract (one edit
per message) worked — but the 2026-10-19 translation-refresh feature flips
sent rows back to `edit_pending` for a SECOND edit round, and that round
found no mapping and fell back to fresh-message mirrors → duplicate bubbles.
Render-log proof (article 351613): 09:06:35 fresh mirror "detected",
09:07:05 in-place edit "published" (mapping consumed), 09:13:34 + 09:13:36
two FRESH sends for the refresh round. Both outbox rows carried the SAME
messenger messageId the whole time, so the messenger side was always one
message — only the whatsapp mirror duplicated.

Contract now (`service.go`): mirror mappings (`waEdits`/`dcEdits`,
`map[string]mirrorEdit{id, at}`) are KEPT across edits — lookups never
consume. A mapping is only replaced when a fresh mirror had to be sent, so
later edits follow the newest bubble:

- whatsapp past `whatsapp.EditWindow` (whatsmeow's 20-min server window,
  re-exported in `internal/whatsapp`): logged loudly, fresh mirror, re-point.
- whatsapp in-place edit failed: logged loudly, fresh mirror, re-point.
- no mapping recorded (send never recorded one, or lumen restarted — the map
  is in-memory only): logged loudly, fresh mirror, re-point. A restart
  between send and a later edit round costs at most one extra bubble.
- discord has no edit window: it falls back only on a real PATCH failure.
- `mirrorEditTTL` (24 h) prunes old entries so the maps stay bounded.

Tested in `internal/bridge/service_mirror_edit_test.go` (lookup never
consumes, re-point replaces, prune, discord repeated edits patch the same
message, failure re-points).

Live-verified 2026-10-20 on the deployed service (`50ec70a`, lumen-sg) with
fake outbox pairs through the real bnp route:

- within-window refresh (`mirrorcheck-b`): one fresh whatsapp send
  (`detected` round1), then THREE in-place edits of the same bubble id
  `3EB0A5BE3E0719FDB461D4` (`published` round1, `detected` round2,
  `published` round2) — zero fresh sends after the initial one. Both outbox
  rows stayed `sent` with the same messenger messageId throughout. Pre-fix
  the refresh round produced two fresh bubbles (the incident).
- past-window refresh (`mirrorcheck-1019a`, 14h22m later): the loud
  "mirror is 14h22m28s old (past the 20m0s edit window), mirroring fresh"
  line fired, one fresh bubble was sent, the mapping was re-pointed, and the
  next edit round (same second, published row) edited that fresh bubble in
  place — no further duplicate.

## Neon storage metering v3.2 (2026-10-02)

- Neon migrated free plans to v3.2 metering: the org consumption endpoint's
  legacy `peak_data_storage`/`data_storage` fields now return 0 and the live
  components moved to `v3_metrics` (`root_branch_logical_size`,
  `root_branch_history_size`, `child_branch_change_size`). The storage warning
  read the legacy fields only → after the migration it silently read 0%
  forever. The 07:12 UTC warning batch (5 projects fired in the same 6
  seconds, lumen at 419.86 MB / 82%) was the last legacy reading — lumen's
  actual storage right after was ~38 MB (7.5%); DB tables measure 12 MB. The
  419 MB was branch history (retention now shows 6h) reclassified by the
  migration, not table growth.
- Fix: `resolveNeonStorage` (internal/notify/neonusage.go, tests in
  neonusage_test.go) prefers the legacy fields (v3.1 orgs unchanged) and
  falls back to summing exactly those three v3 metrics; unknown metric names
  are ignored so a future metric can't inflate the figure.

## Vision: image descriptions via direct HTTP to opencode zen (2026-10-05)

- Goal: make `llm` (text-only `Atria-Dawn-Preview`) "see" image attachments
  (Discord today). Top-level config section `vision:` + `internal/vision`
  package + `agent.Runner.describeImages` (internal/agent/agent.go), which
  swaps every `image_url` content part for a `[attached image N of M]
  <description>` text part before the chat model is called. Image parts with
  `vision.enabled: false` FAIL LOUDLY instead of being dropped.
- **No opencode CLI anymore** (old `opencode run -f` path removed
  2026-10-05): the describer POSTs straight to
  `POST <vision.base_url>/chat/completions` (default
  `https://opencode.ai/zen/v1`, OpenAI-compatible, model
  `mimo-v2.6-flash-free`, `stream:true` + SSE parse). The CLI path needed a
  185MB binary in the image AND peaked at ~716 MB RSS - bigger than Render
  free's 512MB total, so the first image would OOM-kill lumen; plain HTTP is
  a few MB, so `vision.enabled` is TRUE in production.yaml again. Dockerfile
  no longer installs opencode.
- **The zen free-tier gate, corrected (re-bisected 2026-10-16 against fresh
  captures of opencode v2.0.23 traffic - this supersedes the 2026-10-05 and
  2026-10-14 notes):**
  1. It is not headers/TLS/IP. The body + ONE identity variable decide it:
     the session header group. `x-opencode-session`, `x-opencode-session-id`,
     `x-session-affinity` and `x-session-id` must all carry the same session
     id in the scheme the installed client generates (packages/schema/src/
     identifier.ts: `Date.now()*0x1000 + counter`, bitwise-negated for
     sessions ("descending"), 12 hex chars + 14 base62; implemented by
     `zengate.SessionID()`). Random hex in the same shape -> 403; a
     generated id -> 200 (interleaved A/B, 3/3 vs 3/3, plus 2 fresh
     generated ids run live). This is the check that broke every caller on
     2026-10-14 - the bodies were never the problem, and the "attested
     client required" verdict was wrong.
  2. The body needs ONE of two shapes: the main-agent branch (any `tools`
     array present; a 2-tool subset passes, so it is the array's presence,
     not its contents) or a registered prompt prefix (the old title-prompt
     recipe still passes with a valid session id, verified 2026-10-16, but
     the main-agent shape has no byte-exact dependency - the prompt capture
     was DELETED). Free system content + no tools -> 403. `"stream": true`
     is REQUIRED on both branches (removing it -> 403);
     `stream_options.include_usage` is not required but harmless.
  3. `tool_choice: "none"` passes (verified) - keeps the model from emitting
     tool calls on the tools-bearing branch. Vision requests pass the same
     way (image content parts fine: data-URL png -> 200 + correct
     description with the new envelope, re-verified live 2026-10-16).
  4. Intermittent 403 bursts happen regardless -> `vision.max_attempts`
     (default 3, 1s/3s backoff) / the zen lane's own cadence; the final
     error carries the HTTP status + response body, so a burn-out is loud,
     never silent. A single 403 is ambiguous (throttle vs shape) - settle it
     with the live tests, not guesses.
- Recipe lives in `internal/vision/vision.go` + `internal/zengate/`:
  system = a plain compiled-in vision instruction (content is free on this
  branch); body = tools array (`zengate.Tools()`, embedded from the capture)
  + `tool_choice: "none"` + `max_completion_tokens: 32000` + `store: false`;
  request headers: `Authorization: Bearer public` (the anonymous key),
  `User-Agent: opencode/latest/2.0.23/cli`, `x-opencode-client: cli`,
  `x-opencode-project: <fresh 40-hex>` (`zengate.ProjectID()`), all four
  session headers = one fresh `ses_` id (`zengate.SessionID()`), fresh
  `b3`/`traceparent` (`zengate.TraceHeaders()`). No `x-opencode-request`
  header (the captured client sends none on this route).
- **The prompt-capture era is OVER (2026-10-16)**:
  `internal/zengate/zengate-title-prompt.txt` and the repo `.gitattributes`
  are DELETED. History: the gate follows the prompt the INSTALLED client
  registers, so the capture had to be re-extracted on every opencode bump
  (v1.18.25 -> v2.0.23 was one such break: 2026-10-16 ~06:00 every Describe
  started failing `403 FreeTierError` until the prefix was refreshed from
  the v2 binary). The main-agent branch needs no prompt bytes at all, so
  that whole class of breakage is gone. If 403s return after a future
  client update, re-bisect with the replay harness (recipe in
  `automata-private/opencode.ai/AGENTS.md`).
- Config fields (`vision:`): `enabled`, `base_url`, `api_key` (default
  `public`), `user_agent`, `model`, `prompt` (the USER text sent next to
  the image), `timeout`, `max_attempts`, `proxy_url` (optional egress).
  The system prompt is intentionally NOT configurable (compiled in,
  `visionSystemPrompt`). `prompt_context.go` reports `Vision engine: <model>`.
- **Datacenter egress: TESTED OK (2026-10-05)** - the gate accepts Render's
  egress. Full Discord E2E from the live service: image message in the test
  channel (1537650032441032765) -> `describeImages` HTTP from the Render
  container -> 200 (no FreeTierError, no proxy) -> reply landed describing
  the solid red PNG as "a solid deep crimson rectangle... flat color
  swatch". `proxy_url: socks5://127.0.0.1:1055` was NOT needed and stays
  off; if a future 403 burst ever becomes permanent, that remains the flip.
- **Gotcha - silent run death (FIXED 2026-10-05)**: the first vision E2E
  attempt ("describe the attached image") streamed thinking ("there's a
  vision tool available...") and then the run ended with NO final reply and
  NO [chat error] - placeholder deleted, channel blank. Same death on a text
  `/ai /status` prompt. Two causes, both closed:
  1. `internal/discordbot/service.go` collapsed `<NO_REPLY>` and empty
     replies into ONE silent-discard branch, so any turn where the model
     ended with reasoning + tool calls but no final text
     (`empty_reply` / `no_assistant_message` per `classifySilentTurn`)
     vanished with zero trace. Now only `<NO_REPLY>` (model explicitly
     choosing silence) discards; an empty non-silent turn goes loud through
     `emptyReplyNoticeText` -> `formatRunErrorForDiscord`
     ("run ended without a reply (empty_reply)") + still audits.
  2. `agent.Run`'s model/tool loop was UNBOUNDED - `app.max_agent_loops`
     (documented "model/tool rounds per turn", default 12, production 32)
     was defined + validated but NEVER READ, so a hallucinated-tool loop
     could run until the turn died. Now counted: past the cap Run returns
     "agent stopped after N model/tool rounds (app.max_agent_loops=N)" ->
     the existing loud error-reply path.
  Tests: `TestRunStopsAtMaxAgentLoops`, `TestEmptyReplyNoticeTextIsLoudAndNamesReason`.
  Note: `/ai /status`-shaped TEXT prompts in Discord never dispatch commands -
  discordbot registers slash APPLICATION commands (`syncCommands`); text
  command dispatch is bridge-only (whatsapp/messenger). Such text always
  falls through to the agent.
- Live check (costs one free-tier call, ~45s):
  `OPENCODE_VISION_LIVE=1 go test -count=1 -run TestDescribeLive ./internal/vision/`
  - draws a solid red PNG, expects the description to say "red". Unit tests
  cover request shape (identity headers, prefix-exact system prompt,
  stream flag), retries, loud failures, SSE parsing and image loading
  against httptest - no network needed.

## Tech News multi-feed notifications & command toggle (2026-10-26)

- Added multi-feed tech news poller in `internal/notify/technews.go` supporting Ars Technica, The Verge, BleepingComputer, and Hacker News.
- Deduplication: tracked via Neon DB table `public.tech_seen (guid, source, title, seen_at)`.
- Anti-spam baseline seeding: on startup (first pass 5s after boot), if a feed has 0 seen items in `public.tech_seen`, it seeds all currently active items so old backlog is never blasted to the thread. Subsequent ticker intervals (default `15m`) post only newly published articles.
- Commands: `/notify tech [on|off]` (aliases: `technews`, `news`) toggles tech news notifications on the active chat thread. Supported across WhatsApp and Messenger.
- Daily delivery cap: `notify.tech_news.max_per_day` (default 3) caps deliveries per thread per calendar day in Asia/Dhaka (+06:00) using Neon table `public.tech_deliveries (thread_id, guid, delivered_at)`. Items past the cap are still marked in `public.tech_seen` to prevent stale backlogs.
- Automated notification awareness: `bridge.Service.recordNotificationInSession` records outbound notifications (tech news, games, alerts) into `s.sessions[<platform>:<threadID>]` and the shared conversation memory shard as assistant messages. When a user replies or asks about a notification, the model has the notification's full text in context.
- Gotchas:
  - `The Verge` RSS feed endpoint is `https://www.theverge.com/rss/index.xml` (`/tech/rss/index.xml` returns 404).
  - When adding pollers to `internal/notify`, wire config in `cmd/element-orion/main.go` `buildNotify` function, otherwise `notify.Config` omits the section and defaults to disabled.

## Messenger reactions support (2026-10-29)

- Added symmetric Messenger reaction support modeled directly after `add_discord_reaction`:
  - Tool: `add_messenger_reaction` in `internal/tools/messenger.go` takes `emoji` (required), `message_id` (optional, defaults to triggering message in context), and `thread_id` (optional, defaults to triggering thread in context).
  - Tool Context: `tools.WithMessengerToolContext` injects `ThreadID`, `UserID`, and `MessageID` into context during Messenger agent runs (`bridge.handleMessengerMessage`), allowing the model to react to the incoming user message without manually specifying the thread or message ID.
  - Allowlist Gate: `cfg.MessengerThreadAllowedTarget(targetThreadID, activeThreadID)` verifies the thread is allowed by config or matches the currently active thread context.
  - Outbound Execution: `messenger.Client.SendReaction(ctx, threadID, messageID, reaction)` uses `socket.SendReactionTask` with retry/reconnect through `executeRetry`.
  - Inbound Observability: `messenger.Client` parses `*table.LSTable` events for `LSUpsertReaction` and `LSDeleteReaction`, logs reactions clearly, and fires a registered `ReactionHandler` (`bridge.handleMessengerReaction`) for incoming user reactions.
  - Configuration: `add_messenger_reaction` added to default enabled tools in `internal/config/config.go`, `config/production.yaml`, and `config/element-orion.example.yaml`.

## Incoming reaction handling and autonomous silence support (2026-10-29)

- Inbound reaction awareness across platforms:
  - Discord: registered `handleMessageReactionAdd` with gateway intents `IntentsGuildMessageReactions` and `IntentsDirectMessageReactions`. Ignores bot's own reactions. In guild channels, only activates if reacting to the bot's own message (prevents noisy channel spam). Passes `[reaction] @user reacted with <emoji> to your message: "<text>"` as an inbound prompt with `promptKindReaction`.
  - Messenger: `handleMessengerReaction` feeds non-empty incoming user reactions to `s.agentRun` with `tools.WithMessengerToolContext` for tool context defaults.
- Autonomous agent response & silence:
  - No rigid "never reply" constraints in prompt: the model has full autonomy to reply with text, react back using tools (`add_discord_reaction` / `add_messenger_reaction`), or stay silent using the `<NO_REPLY>` token.
  - Silent turns (`<NO_REPLY>`) discard without errors or stray messages on both Discord (`anim.discard()`, no thinking anim created for `promptKindReaction`) and Messenger (skips `thinking.` message upfront on `[reaction]` turns; handles `<NO_REPLY>` cleanly without empty reply error).
  - Reaction events and decisions persist into conversation history so the model organically remembers user reactions when asked later.

Upstream is `PUT-Request/Element-Orion` (canonical as of 2026-09-04; the old
path `eli32-vlc/lumen-agent` redirects — the author moved all their projects
into the new `PUT Request` org that day and restored the original
Element-Orion name; upstream's last commit is 2026-05-15, so there is nothing
to rebase from). This fork is `FahadBinHussain/lumen-agent`.
All merge work is additive (new internal packages + config fields) — no upstream
files modified except `cmd/element-orion/main.go` (bridge wiring) and
`internal/config/config.go` (new sections). Rebase/merge from upstream stays
straightforward.
