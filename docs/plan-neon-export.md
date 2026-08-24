# Plan: Neon Export (encrypted, same repo, single file)

## Goal

When a Neon org's consumption hits >= 90 CU-hours, export **all databases in
every over-threshold org** as encrypted dumps, push them to the same lumen repo
on a dedicated branch, and overwrite the previous export each poll so storage
doesn't stack. This covers the "multiple neons over 90" case — every org over
threshold re-exports every poll cycle.

## Design

### Trigger — every poll, NOT deduped

In `internal/notify/neonusage.go`, `checkNeonUsage` already fires when
`Used >= WarningHours` (default 90). No new poller, no new interval — the
export piggybacks on the existing 1-hour poll.

**Critical: the export must NOT go through `sendNeonWarning`'s dedupe.**
`sendNeonWarning` dedupes on `neonState[projectID] == resetDate`
(neonusage.go:83) — it fires **once per quota-reset period** (monthly), which
is correct for a notification but wrong for a backup. If the export reused
that path, you'd get one stale backup per month.

The export is gated on the **threshold check only** (`r.Used >= WarningHours`),
so it re-runs on **every poll cycle while the org stays over threshold**.
Each poll: fresh dump of **every project in every over-threshold org** →
encrypt → force-push over the previous exports. The branch still holds exactly
one file per project; the newest overwrite is the latest backup. Storage stays
flat, backup stays current.

The dedupe state (`neonState`) is used ONLY for the notification — the export
reads its own flag `cfg.NeonUsage.Export.Enabled` and ignores `neonState`
entirely.

**Multiple over-threshold orgs:** the watcher already iterates every org under
every configured API key (neonusage.go:39-56). Each org over threshold triggers
an export of ALL its projects — so if 3 orgs are over 90, all 3 get re-exported
every poll. The export loop runs for the full set of over-threshold orgs, and
ALL resulting files go into one git commit before the single force-push.

### Export flow (per over-threshold org, ALL its projects)

1. **List projects in the org** — `GET /v2/projects?org_id=<orgID>&limit=200`
   using the same Neon API key the watcher already has. Returns every project
   under that org (an org can hold multiple projects, e.g. cbexdac's three
   `blindspot` projects).

2. **Per project — get connection URI** — `GET /v2/projects/{id}/connection_uri?database_name=neondb&role_name=neondb_owner`.
   Returns `{"uri": "postgresql://..."}`.

3. **Per project — pg_dump** — run `pg_dump --no-owner --no-privileges --format=plain <uri>`
   via `os/exec`. Pipe to gzip, then encrypt with openssl.

4. **Per project — encrypt** — `openssl enc -aes-256-cbc -pbkdf2 -salt -pass pass:<key>`
   where `<key>` comes from env var `LUMEN_EXPORT_KEY` (Render env, never
   committed). Output file: `/tmp/<project-id>.sql.gz.enc`.

5. **Push to repo** — after ALL over-threshold orgs/projects have exported,
   one git init + orphan branch + force-push bundles every file. Steps:
   - `git init /tmp/export-repo`
   - `git checkout --orphan exports`
   - copy each `.enc` file to `backups/<project-id>.sql.gz.enc`
   - `git add . && git commit -m "export <n> project(s) <timestamp>"`
   - `git remote add origin https://x-access-token:<TOKEN>@github.com/FahadBinHussain/lumen-agent.git`
   - `git push --force origin exports:exports`
   - `rm -rf /tmp/export-repo`

   Each push rewrites the `exports` branch to contain exactly one commit with
   one file per project. Reachable storage stays at ~max(export size). Old
   blobs become unreachable; GitHub GCs eventually.

### Branch: `exports`

- The `exports` branch is never merged into `main` — no auto-deploy trigger.
- Contains one file per project, named `backups/<project-id>.sql.gz.enc`.
- Each export force-overwrites the file for that project. If multiple projects
  are over threshold, the branch has one file per project.

### What gets into the repo

```
exports/
  backups/
    aged-fire-12399795.sql.gz.enc   # lumen
    round-field-05224978.sql.gz.enc # the-daily-times
    ...
```

Each file is the encrypted dump of ONE project (all projects in every
over-threshold org). The `exports` branch is public (public repo), but the
content is encrypted — only someone with the `LUMEN_EXPORT_KEY` can decrypt.

### Config additions

In `internal/config/config.go`, extend `NotifyNeonUsage`:

```yaml
notify:
  neon_usage:
    enabled: true
    warning_hours: 90
    export:
      enabled: true
      repo: "FahadBinHussain/lumen-agent"
      branch: "exports"
      path: "backups"  # directory within repo
      key_env: LUMEN_EXPORT_KEY
      github_token_env: LUMEN_EXPORT_GITHUB_TOKEN
```

New struct `NotifyNeonExport` with defaults:
- `enabled: false` (opt-in)
- `repo: "FahadBinHussain/lumen-agent"`
- `branch: "exports"`
- `path: "backups"`
- `key_env: "LUMEN_EXPORT_KEY"`
- `github_token_env: "LUMEN_EXPORT_GITHUB_TOKEN"`

### Dockerfile changes

Add to the `debain:bookworm-slim` layer:

```dockerfile
RUN apt-get install -y --no-install-recommends postgresql-client git openssl
```

- `postgresql-client` provides `pg_dump`
- `git` for the orphan-branch force-push
- `openssl` already ships with debian but install explicitly

### Env vars on Render

| Var | Purpose |
|---|---|
| `LUMEN_EXPORT_KEY` | Encryption key for openssl (long random string) |
| `LUMEN_EXPORT_GITHUB_TOKEN` | GitHub PAT with `contents:write` on lumen-agent repo |

The GitHub token is the same `fahadbinhussain@outlook.com` PAT already in the
vault (rule 34). Only the last 4 chars are logged for verification.

### Code structure

New file: `internal/notify/neonexport.go`

```
func (s *Service) exportNeonOrg(ctx context.Context, apiKey, orgID string) error
```

Steps:
1. List projects in the org via `GET /v2/projects?org_id=<orgID>`
2. For each project: fetch connection URI, `pg_dump | gzip | openssl` → `/tmp/<project-id>.sql.gz.enc`
3. After ALL projects from ALL over-threshold orgs are collected, one git orphan-init + commit + force-push to `exports`
4. Clean up temp files

The export is called **synchronously** (no goroutine) from `checkNeonUsage` — the
loop is restructured so the export call stays inside the key loop where `apiKey`
is in scope, and all over-threshold orgs are collected before the single push:

```go
func (s *Service) checkNeonUsage(ctx context.Context) error {
    // ... (existing setup, state load) ...

    var exportFiles []exportFile  // collect encrypted files for batch push
    for _, envName := range s.cfg.NeonUsage.APIKeyEnv {
        key := strings.TrimSpace(os.Getenv(envName))
        if key == "" { continue }
        orgs, err := s.neonOrgs(ctx, key)
        if err != nil { return fmt.Errorf("%s: orgs: %w", envName, err) }
        for _, org := range orgs {
            u, err := s.neonConsumption(ctx, key, org.ID)
            if err != nil { return fmt.Errorf("%s: %s consumption: %w", envName, org.ID, err) }
            if u.Used >= s.cfg.NeonUsage.WarningHours {
                overThreshold++
                s.sendNeonWarning(ctx, u)     // deduped once per reset period
                if s.cfg.NeonUsage.Export.Enabled {
                    files, err := s.exportNeonOrg(ctx, key, org.ID)
                    if err != nil { log.Printf("notify: export %s: %v", org.ID, err) }
                    exportFiles = append(exportFiles, files...)
                }
            }
        }
    }

    // Single push: batch ALL over-threshold orgs' projects into one commit
    if len(exportFiles) > 0 {
        if err := s.pushExports(ctx, exportFiles); err != nil {
            log.Printf("notify: push exports: %v", err)
        }
    }
    return nil
}
```

`exportNeonOrg` returns a list of `exportFile{srcPath, destName}` without
pushing. `pushExports` does the git init + orphan branch + force-push. This
keeps the push logic separate and ensures exactly one commit per poll cycle.

### Edge cases

- **No key / no token** — log a warning, skip export (not a hard error, the
  warning notification still fires).
- **pg_dump fails** — log error, don't retry (next poll cycle will try again).
- **ephemeral /tmp** — Render's layer is writable; `/tmp` survives for the
  duration of the run. Cleanup after push.
- **Multiple over-threshold orgs** — the loop iterates every org under every
  API key; each org over threshold exports ALL its projects. All collected
  files go into ONE git commit + force-push per poll cycle, so the `exports`
  branch always holds every over-threshold project in a single commit (no
  per-org commits stacking on the branch).
- **Org with multiple projects** — `exportNeonOrg` lists the org's projects
  and dumps each one; e.g. an org with 3 `blindspot` projects produces 3
  files in the same push.
- **Export cadence while over threshold** — every 1-hour poll re-exports and
  force-pushes every over-threshold org, so each backup is at most ~1h stale.
  That's the intent (latest backup on over-threshold projects). If hourly
  force-pushes are too chatty, a future `export_interval` config can throttle
  it; not in scope for v1.
- **Export of a project that's already at 100% CU** — still works; the DB is
  readable (Neon doesn't lock the DB at 100 CU-hours, it just stops compute;
  but the free plan's compute limit may suspend the endpoint when exhausted).
  If the endpoint is suspended, pg_dump hangs; add a timeout (60s) and skip
  on timeout.

### Storage analysis (from actual data)

All 20 projects across all accounts are well under the 100 MB per-file limit:

| Largest | Storage used |
|---|---|
| the-daily-times | 45.9 MB |
| Daily-BNP | 44.8 MB |
| lore | 39.7 MB |
| ... (rest under 39 MB) | |

A gzipped pg_dump is smaller than synthetic_storage_size (no indexes, no WAL).
So the `exports` branch will stay well under 100 MB per file, and the total
reachable branch size is the sum of all project exports (few hundred MB max).

### Implementation order

1. Add `export` sub-config to `NotifyNeonUsage` in `internal/config/config.go`
2. Add defaults in `config.go` defaults block
3. Thread `Export` through `buildNotify` in `cmd/element-orion/main.go`
4. Create `internal/notify/neonexport.go` with the export function
5. Wire call in `checkNeonUsage` (neonusage.go)
6. Update `config/production.yaml` with `export.enabled: true`
7. Update Dockerfile to add `postgresql-client git openssl`
8. Set `LUMEN_EXPORT_KEY` and `LUMEN_EXPORT_GITHUB_TOKEN` on Render
9. Build, test, deploy