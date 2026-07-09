# Deployment

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
and keeps `need_update_to` null/undef. Ansible clears `need_update_to` directly
through `Core::Config::set_value` and never triggers the UI save path.

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
5. Wait until no active `srv_customlab_nalog.cgi` process in `shm-spool-1`
6. Pause `shm-spool-1`
7. Re-check `state.json` for receipts with status `creating`
8. Back up `state.json`, Compose, and manifest under a timestamped release directory (mode `0600`)
9. Atomically replace Compose and run `docker compose up -d --no-deps --pull never vff-fiscal`
10. Health check `/healthz` and authenticated `/v1/user` smoke test
11. Write `/opt/vff-fiscal/deploy-state.json` atomically on success only
12. Always unpause `shm-spool-1`

Idempotent re-deploy of the same running image skips steps 5–10 (no spool pause, no backups).

### Adapter deploy ordering

1. Stage CGI and helper modules under `/opt/shm/pay_systems/.vff-fiscal-stage/<sha>/`
2. Compile staged files in `shm-core-1` and `shm-spool-1`
3. Wait until no active adapter CGI process in `shm-spool-1`
4. Pause `shm-spool-1` and confirm paused
5. Back up live CGI, helper modules, ownership/modes/immutable metadata
6. Clear `need_update_to` via `Core::Config::set_value` (not SHM UI)
7. Verify `need_update_to` is null/undef
8. Remove `chattr +i`, atomically install helpers and CGI
9. Compile installed files in both containers
10. Restore previous `enabled` unless `vff_fiscal_adapter_enabled` is set
11. Restore `chattr +i`, verify immutable flag and live SHA256
12. Run safe adapter smoke tests
13. Always attempt `chattr +i`, then unpause spool only when immutable flag is present

At no point may `shm-spool-1` run while the live CGI lacks `chattr +i`.

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
mounted inside `shm-core-1`.

## Service rollback

Manual service rollback:

- Requires `ROLLBACK_CONFIRM=1` and a service backup directory
- Validates the rollback image exists locally (no rebuild, no pull)
- Pauses spool, waits for quiet CGI, blocks on `creating` receipts
- Backs up current `state.json` as `pre-rollback-state.json` only
- Restores backed-up Compose and runs `docker compose up -d --no-deps --pull never`
- Verifies `/healthz` and authenticated `/v1/user`
- **Never restores `state.json` automatically**
- Always unpauses spool

## Adapter rollback

Manual adapter rollback defaults to `enabled=false` unless
`vff_fiscal_rollback_adapter_enabled=true` is passed explicitly.

Reason: the backup may contain the previous third-party Customlab adapter; enabling
it could send FNS credentials and payment data to the old endpoint.

Rollback restores CGI, both helper modules, ownership, modes, and immutable state.
It clears `need_update_to` and does not restore it from backup.

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
| adapter install/compile failure | Adapter rescue restores backed-up CGI + helpers; `need_update_to` cleared |
| `chattr +i` failure | Spool stays paused until `+i` verified or manual recovery |
| manifest write failure | Deployment marked failed; prior manifest retained |

## SHM adapter token configuration

Precedence:

1. non-empty `client_token` from SHM module configuration
2. non-empty `VFF_FISCAL_API_TOKEN` in SHM containers
3. controlled configuration error

## Payment timestamp

The adapter sends `comment.object.captured_at` unchanged as `operation_time`.

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
