# Deployment

[Русская версия](DEPLOYMENT.ru.md)

Production deployments are driven by Ansible playbooks in `ansible/` and are
invoked explicitly through Make targets. **Nothing deploys automatically from
GitHub Actions or local `git push`.**

## Architecture

```text
Operator workstation                Production host
---------------------               ----------------
make deploy                         /opt/vff-fiscal/app          (exact Git SHA)
  -> ansible/playbooks/deploy.yml   /opt/vff-fiscal/.env         (secrets)
     1. vff_fiscal_common          /opt/vff-fiscal/data/        (bind-mounted state)
     2. vff_fiscal_service         /opt/vff-fiscal/docker-compose.yml
     3. vff_fiscal_adapter         /opt/vff-fiscal/deploy-state.json
     4. vff_fiscal_status           /opt/shm/pay_systems/...     (SHM adapter)
                                    shm-core-1 / shm-spool-1      (existing SHM stack)
                                    vff-fiscal container          (docker network shm_default)
```

The service container reads `/opt/vff-fiscal/.env` and persists state under
`/opt/vff-fiscal/data/state.json`. The SHM adapter CGI calls
`http://vff-fiscal:8080/v1/receipts` using `client_token` from SHM configuration.

## Operational warning

**Do not save `srv_customlab_nalog` settings through the SHM UI during deployment.**

Saving pay-system settings through the UI calls `Core::Config::updated_pay_systems`,
queues `Cloud::Jobs::job_download_paystem`, and may attempt to replace the custom
CGI from the cloud downloader. Production protects the live CGI with `chattr +i`
and keeps `need_update_to` null/undef. Ansible clears `need_update_to` through
the host-side `shm-config.pl` helper, which loads the `pay_systems` config
service with `get_service('config', _id => 'pay_systems')`, calls
`$config_service->set_value(...)`, and commits through SHM. The helper never
uses a static `Core::Config::set_value(...)` call and never triggers the UI save
path. `clear-update-marker` and `set-enabled` are idempotent: they skip writes
and commits when the requested state is already present.

## Prerequisites

- `ansible-core` 2.16.x on the operator workstation
- `ansible-lint` 6.17.x for local verification
- SSH access to the production host as a privileged user
- Private inventory copied from the example:

```bash
cp ansible/hosts.ini.example ansible/hosts.ini
```

Edit `ansible/hosts.ini` locally with the real host name and SSH settings.
Never commit `ansible/hosts.ini`.

SSH host-key verification is enabled. Before the first connection, obtain the
host key out of band, compare its fingerprint with the production operator, and
only then add it to the controller's `known_hosts`. For example, capture a
candidate with `ssh-keyscan <host>` and verify the fingerprint manually before
installing it. Automation never accepts an unknown host key automatically.

## First-time setup

1. Create `/opt/vff-fiscal`, `/opt/vff-fiscal/data`, and `/opt/vff-fiscal/backups/releases`.
2. Place production `.env` at `/opt/vff-fiscal/.env` (mode `0600`).
3. Initialize or copy `state.json` into `/opt/vff-fiscal/data/state.json`
   (owned by UID/GID `65532`, mode `0600`).
4. Ensure Docker network `shm_default` exists and SHM containers `shm-core-1` and
   `shm-spool-1` are running.
5. Configure SHM `client_token` to match `VFF_FISCAL_API_KEY`.
6. Copy `ansible/hosts.ini.example` to `ansible/hosts.ini` locally.

## Version policy

Deployments require an explicit 40-character lowercase commit SHA:

```bash
make deploy HOST=<inventory-host> VERSION=<40-char-sha>
```

After `git fetch`, Ansible verifies:

- the commit object exists
- checkout `HEAD` equals the requested SHA
- the server working tree is clean
- the commit is reachable from `origin/main`

Deploying a commit not reachable from `origin/main` requires an explicit override:

```bash
make deploy ... EXTRA='-e vff_fiscal_allow_unreachable_commit=true'
```

Branch names, tags, floating refs, and Make defaults from the local `HEAD` are rejected.

### One-Time Legacy Image Bootstrap

Older production hosts may still run `vff-fiscal:prod`, which is mutable and
cannot be used as a safe rollback target. The service role rejects mutable
previous images unless an explicit one-time bootstrap is requested.

For the first automated service deployment only, provide the exact commit that
produced the currently running legacy image:

```bash
make deploy-service HOST=vff-fiscal VERSION=<new-40-char-sha> \
  EXTRA='-e vff_fiscal_allow_legacy_image_bootstrap=true -e vff_fiscal_legacy_image_commit=<legacy-40-char-sha>'
```

During this bootstrap Ansible does not pull or rebuild the legacy image. It
reads the currently running container image ID, creates a local immutable alias
`vff-fiscal:<first-12-chars-of-legacy-commit>` pointing to that exact image ID,
verifies the alias, and writes the rollback Compose backup with the immutable
alias instead of `:prod`. Backup metadata records `legacy_bootstrap=true`, the
original mutable tag, the immutable alias, the explicit legacy commit, and the
image ID.

Never infer that `vff-fiscal:prod` matches the checkout on disk. If the legacy
commit is wrong, rollback metadata will be wrong. Once `deploy-state.json`
exists, the bootstrap path is refused; ordinary deployments again require the
previous running service image to use a commit-derived immutable tag.

## Deployment Lock

Mutating playbooks (`deploy`, `deploy-service`, `deploy-adapter`,
`rollback-service`, and `rollback-adapter`) acquire the same host-side atomic
lock directory at `/opt/vff-fiscal/deploy.lock`. The read-only status playbook
does not use the lock.

The lock metadata contains only safe operational data: operation type, timestamp,
controller hostname, process ID, and a random ownership token. A second mutating
operation fails immediately with the existing lock metadata rather than waiting.
The lock is released in an `always` path and only when the token matches, so one
operation does not delete another active operation's lock.

If an operator has verified that no deployment or rollback process is running
and the lock is stale, inspect `/opt/vff-fiscal/deploy.lock/metadata.json`, then
remove `/opt/vff-fiscal/deploy.lock` manually. Do not remove the lock while an
Ansible process is active.

## Make targets

| Target | Purpose |
|--------|---------|
| `verify` | Syntax check, ansible-lint, Go tests |
| `deploy` | Service, then adapter, then status |
| `deploy-service` | vff-fiscal service only |
| `deploy-adapter` | SHM adapter only |
| `deploy-status` | Read-only safe status report |
| `rollback-service` | Manual service rollback from backup dir |
| `rollback-adapter` | Manual adapter rollback from backup dir |

Requirements:

- `HOST` is required for all deployment and rollback commands
- `VERSION` is required for deploy commands
- `ansible/hosts.ini` must exist (Make fails clearly if missing)
- Rollback requires `ROLLBACK_CONFIRM=1`

Examples:

```bash
make deploy HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-service HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-adapter HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-status HOST=vff-fiscal
make rollback-service HOST=vff-fiscal BACKUP_DIR=/opt/vff-fiscal/backups/releases/<release>/service ROLLBACK_CONFIRM=1
make rollback-adapter HOST=vff-fiscal BACKUP_DIR=/opt/vff-fiscal/backups/releases/<release>/adapter ROLLBACK_CONFIRM=1
```

Pass extra Ansible arguments with `ANSIBLE_FLAGS` or `EXTRA`.

Local controller cleanliness is checked by default. Exceptional override:

```bash
make deploy VFF_FISCAL_ALLOW_DIRTY_LOCAL_CONTROLLER=1 ...
```

This does **not** bypass server-side SHA, reachability, or clean-checkout checks.

## Exact deployment order

### Combined deploy (`make deploy`)

1. Common preflight (SHA validation, git checkout, host probes)
2. Service role
3. Adapter role
4. Status role (read-only verification)

### Service deploy ordering

1. Validate `.env` keys and `state.json` structure (without blocking on `creating`)
2. Build image `vff-fiscal:<12-char-sha>` if missing or revision label mismatch
3. Validate image exists locally and carries OCI label `org.opencontainers.image.revision`
4. Render and validate Compose candidate (`docker compose config -q`, no `build:` section)
5. Use `docker top` to wait until no active `srv_customlab_nalog.cgi` process
6. Detect whether spool was already paused; pause it only when this operation owns the pause
7. Confirm paused and immediately repeat `docker top` (which works while paused)
8. If a process appeared in the race window, unpause only an operation-owned
   pause and retry the complete gate for a bounded number of attempts
9. Re-check `state.json` for receipts with status `creating`
10. Back up `state.json`, Compose, and manifest under a timestamped release directory (mode `0600`)
11. Atomically replace Compose and run `docker compose up -d --no-deps --pull never vff-fiscal`
12. Health check `/healthz` and authenticated `/v1/user` smoke test
13. Write `/opt/vff-fiscal/deploy-state.json` atomically on success only
14. Unpause only when this operation paused spool, then verify `State.Paused=false`

Idempotent re-deploy of the same running image skips steps 5–10 (no spool pause, no backups).

### Adapter deploy ordering

1. Stage CGI and helper modules under `/opt/shm/pay_systems/.vff-fiscal-stage/<sha>/`
2. Compile staged files in `shm-core-1` and `shm-spool-1`
3. Record staged and active checksums; verify immutable protection, SHM safe
   status, and the no-action diagnostic response
4. Decide `adapter_cutover_required`; skip the cutover block when everything
   already matches
5. **Idempotent path:** accept deployment explicitly, skip spool pause, backup,
   `chattr` changes, and live file replacement; update the manifest to the
   requested commit while preserving the existing `backup_directory` from the
   last real cutover
6. **Cutover path:**
   1. Acquire the bounded `docker top` quiet/pause/post-pause gate
   2. Reject the operation if spool was already paused by an operator
   3. Back up live CGI, helper modules, ownership/modes/immutable metadata, and
      safe SHM status
   4. Remove `chattr +i`, verify `lsattr` reports `immutable=0`, and set only
      `adapter_immutable_removed=true`
   5. Clear `need_update_to` through `shm-config.pl`, verify it remains absent
   6. Set `adapter_files_modification_started=true` immediately before the first
      active file replacement
   7. Atomically install helper modules and CGI
   8. Compile installed files in `shm-core-1`, restore `enabled` unless
      overridden, and verify active checksums against staged source
   9. Run the core diagnostic smoke test while spool remains paused
   10. In `always`: restore `chattr +i`, verify immutable protection, and unpause
       only an operation-owned pause
   11. Set `adapter_post_unpause_validation_required=true` after successful cutover
   12. Post-unpause validation (stable gate, not tied to mutable spool ownership
       facts): compile in the running spool, run missing-payment smoke, then run
       authenticated `/v1/user` smoke through `shm-auth-smoke.pl` (which
       initializes SHM before `get_service`)
   13. Set `adapter_deployment_accepted=true` only after post-unpause validation
       succeeds

At no point may `shm-spool-1` run while the live CGI lacks `chattr +i`.
Adapter deploy and rollback refuse live file mutation when the gate reports that
spool was already paused by an operator; the operator's paused state is
preserved and no SHM config, `chattr`, file replacement, or success manifest is
written.

Configuration failures before the first active file replacement do not trigger
transactional file restoration; immutable protection is restored and the play
fails clearly. Failures during or after file replacement use the full rescue
path.

If post-unpause validation fails, rescue sets
`adapter_post_unpause_validation_failed=true`, restores the previous CGI and
both helpers through `restore_adapter.yml`, and fails the play. Restoration
cannot fall through to `deployment_status=success`.

### Success manifest acceptance

A success manifest is written only when `adapter_deployment_accepted=true`.

Accepted paths:

- **Idempotent adapter deploy:** active files, immutable protection,
  `need_update_to`, and diagnostics already match staged content
- **Successful cutover:** core validation, immutable restoration, spool unpause,
  spool Perl checks, missing-payment smoke, and authenticated user smoke all pass

The manifest is **not** prepared or written when any of these are true:

- `adapter_cutover_failed`
- `adapter_post_unpause_validation_failed`
- `adapter_restoration_failed`

A fail-closed guard also stops the play before manifest preparation when
deployment was not explicitly accepted.

Service idempotent redeploy preserves immutable deployment history in
`deploy-state.json` and reconciles corrupted history from trusted backup metadata
only when all trust checks pass.

### Fail-closed immutable handling

Rescue and `always` blocks attempt to restore the previous CGI and helper modules when
needed, run `chattr +i`, verify the immutable flag with `lsattr`, and unpause spool
only after `+i` is confirmed.

If immutable protection cannot be verified, the play fails explicitly and
**`shm-spool-1` remains paused intentionally**. The failure message includes only
safe recovery commands:

```text
chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi
docker unpause shm-spool-1
```

Verify the CGI SHA256 and run `perl -c` on the CGI and helper modules before
unpausing manually.

SHM Perl helpers (`shm-config.pl`, `shm-auth-smoke.pl`) run from the host via
`docker exec -i ... perl - < script` and do not require `/opt/vff-fiscal` to be
mounted inside `shm-core-1`. Both helpers initialize an SHM context with
`SHM->new(skip_check_auth => 1)` before calling `get_service`.

## Service rollback

Manual service rollback:

- Requires `ROLLBACK_CONFIRM=1` and a service backup directory
- Validates the rollback image exists locally (no rebuild, no pull)
- Validates the backup Compose as a candidate before touching production
- Resolves the `vff-fiscal` service image from the candidate Compose and
  requires it to exactly match backup metadata; when metadata includes an image
  ID, the local image ID must match too
- Acquires the spool gate and blocks on `creating` receipts
- Creates a new rollback-operation backup of current state, Compose, and manifest
- Restores backed-up Compose and runs `docker compose up -d --no-deps --pull never`
- Verifies `/healthz` and authenticated `/v1/user`
- Restores and validates the pre-rollback service if rollback validation fails
- **Never restores `state.json` automatically**
- Never unpauses a spool that was already paused by an operator

## Adapter rollback

Manual adapter rollback defaults to `enabled=false` unless
`vff_fiscal_rollback_adapter_enabled=true` is passed explicitly.

Reason: the backup may contain the previous third-party Customlab adapter; enabling
it could send FNS credentials and payment data to the old endpoint.

Rollback restores CGI, both helper modules, ownership, modes, and immutable state.
It clears `need_update_to` and does not restore it from backup.
Before replacement it creates a fresh safety backup of the active adapter. Both
manual rollback and automatic deployment rescue use the same staged,
checksum-verified restoration transaction. If manual rollback fails, the safety
backup is restored before the play fails.

```bash
make rollback-adapter HOST=vff-fiscal BACKUP_DIR=... ROLLBACK_CONFIRM=1
# explicit enable (dangerous):
make rollback-adapter ... EXTRA='-e vff_fiscal_rollback_adapter_enabled=true'
```

## state.json backup policy

- Validated without printing secret values (version, auth keys present, receipts object)
- Backed up only after spool pause and a passing post-pause `creating` check
- Backups live under `/opt/vff-fiscal/backups/releases/<timestamp>-<sha>/{service,adapter}/`
- Backup files are mode `0600` or stricter and are never committed to Git
- Rescue/rollback never auto-restores `state.json`

## Immutable CGI behavior

The live adapter CGI is protected with `chattr +i` outside the short replacement window.
Ansible removes `+i` only while spool is paused, reinstalls files, recompiles, then restores `+i`.

### Recovery if immutable flag is missing

```bash
docker pause shm-spool-1
chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi
lsattr /opt/shm/pay_systems/srv_customlab_nalog.cgi
docker unpause shm-spool-1
```

### Recovery from a stuck paused spool

```bash
lsattr /opt/shm/pay_systems/srv_customlab_nalog.cgi   # confirm +i present
docker unpause shm-spool-1
docker inspect -f '{{.State.Paused}}' shm-spool-1
```

If deployment failed mid-cutover, inspect `/opt/vff-fiscal/backups/releases/` and
`deploy-state.json` before taking manual action.

## Production Compose image model

Rendered Compose uses an immutable tag only:

```yaml
image: vff-fiscal:<12-char-sha>
```

No `build:` section is present after rendering. Cutover uses `--no-deps` so SHM
containers are never recreated.

## Check mode

`ansible-playbook --check` performs read-only preflight where possible. It does **not**:

- check out Git revisions
- build images
- pause containers
- modify SHM configuration or `chattr` flags
- copy production files
- recreate containers
- write manifests

Runtime Docker/SHM validation still requires a live host check outside check mode.
Skipped mutating tasks have initialized transaction facts, so check mode does not
depend on results from checkout, build, staging, pause, or manifest tasks.

## Backup metadata semantics

Service backup metadata records `target_commit`, `target_image`,
`previous_commit`, `previous_image`, `previous_image_id`, and whether a previous
Compose existed. Adapter backup metadata records the target and previous commits,
all three previous file checksums, previous enabled state, and previous immutable
state. If a previous version predates `deploy-state.json`, its commit is recorded
as `unknown-pre-manifest` rather than being inferred from the new target.

## Failure behavior (summary)

| Failure point | Result |
|---------------|--------|
| git fetch / unreachable SHA | Play fails; no cutover |
| image build failure | Play fails; spool never paused |
| Compose validation failure | Play fails before pause |
| spool cannot pause | Play fails; prior state retained |
| active CGI never finishes | Bounded wait, then fail safely |
| `creating` receipt after pause | Play fails; spool unpaused in `always` |
| state backup failure | Cutover aborted |
| health / auth smoke failure | Service rescue restores previous Compose/image; state not restored |
| adapter install/compile failure | Adapter rescue restores backed-up CGI + helpers when file modification started; `need_update_to` cleared |
| adapter post-unpause validation failure | Previous CGI and helpers restored; play fails; no success manifest |
| `chattr +i` failure | Spool stays paused until `+i` verified or manual recovery |
| manifest write failure | Deployment marked failed; prior manifest retained |

## SHM adapter token configuration

Precedence:

1. non-empty `client_token` from SHM module configuration
2. non-empty `VFF_FISCAL_API_TOKEN` in SHM containers
3. controlled configuration error

## Payment timestamp

The adapter sends `comment.object.captured_at` unchanged as `operation_time`.

## SHM adapter privacy contract

Successful CGI stdout for receipt creation contains only `status` and `msg`.
Receipt UUIDs, print URLs, JSON URLs, and fiscal identifiers embedded in those
URLs are stored in SHM payment metadata (`comment.receiptUuid`, `receiptLink`,
`receiptJsonLink`) and must not appear in spool-persisted adapter output.

## Secrets

Never commit `ansible/hosts.ini`, `.env`, `data/state.json`, backups, or tokens.

Ansible tasks that may expose secrets use `no_log: true`. Smoke tests print only
HTTP status, safe counters, and `token_present=true` style fields.

## Known limitations

- Check mode cannot fully validate Docker/SHM runtime behavior.
- Adapter rollback defaults to disabled even if the backup was enabled.
- Idempotent deploy skips backups when the target image is already healthy.
- Reachability override must be explicit and named.

## Local verification

```bash
make verify
python3 -m unittest discover -s tests/deploy -v
make ansible-syntax
make ansible-lint
```
