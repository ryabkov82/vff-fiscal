# Развёртывание

[English version](DEPLOYMENT.md)

Production-развёртывания выполняются Ansible playbook'ами в `ansible/` и запускаются
явно через Make targets. **Ничего не разворачивается автоматически из GitHub Actions
или локального `git push`.**

## Архитектура

```text
Рабочая станция оператора          Production-хост
---------------------------        ----------------
make deploy                          /opt/vff-fiscal/app          (точный Git SHA)
  -> ansible/playbooks/deploy.yml   /opt/vff-fiscal/.env         (секреты)
     1. vff_fiscal_common           /opt/vff-fiscal/data/        (bind-mounted state)
     2. vff_fiscal_service          /opt/vff-fiscal/docker-compose.yml
     3. vff_fiscal_adapter          /opt/vff-fiscal/deploy-state.json
     4. vff_fiscal_status            /opt/shm/pay_systems/...     (SHM adapter)
                                     shm-core-1 / shm-spool-1      (существующий стек SHM)
                                     контейнер vff-fiscal          (docker network shm_default)
```

Контейнер сервиса читает `/opt/vff-fiscal/.env` и сохраняет состояние в
`/opt/vff-fiscal/data/state.json`. SHM-адаптер CGI вызывает
`http://vff-fiscal:8080/v1/receipts`, используя `client_token` из конфигурации SHM.

## Операционное предупреждение

**Не сохраняйте настройки `srv_customlab_nalog` через UI SHM во время развёртывания.**

Сохранение настроек pay-system через UI вызывает `Core::Config::updated_pay_systems`,
ставит в очередь `Cloud::Jobs::job_download_paystem` и может попытаться заменить
кастомный CGI через cloud downloader. В production live CGI защищён `chattr +i`, а
`need_update_to` остаётся null/undef. Ansible очищает `need_update_to` через
host-side helper `shm-config.pl`, который загружает config service `pay_systems` через
`get_service('config', _id => 'pay_systems')`, вызывает `$config_service->set_value(...)`
и выполняет commit через SHM. Helper никогда не использует статический вызов
`Core::Config::set_value(...)` и не запускает путь сохранения через UI.
`clear-update-marker` и `set-enabled` идемпотентны: они пропускают запись и commit,
если запрошенное состояние уже достигнуто.

## Предварительные требования

- `ansible-core` 2.16.x на рабочей станции оператора
- `ansible-lint` 6.17.x для локальной проверки
- SSH-доступ к production-хосту под привилегированным пользователем
- Приватный inventory, скопированный из примера:

```bash
cp ansible/hosts.ini.example ansible/hosts.ini
```

Отредактируйте `ansible/hosts.ini` локально с реальным именем хоста и SSH-настройками.
Никогда не коммитьте `ansible/hosts.ini`.

Проверка SSH host key включена. Перед первым подключением получите ключ вне полосы
автоматизации, сравните fingerprint с production-оператором и только затем добавьте
его в `known_hosts` контроллера. Например, получите кандидата через `ssh-keyscan <host>`
и вручную проверьте fingerprint перед установкой. Автоматизация никогда не принимает
неизвестный host key автоматически.

## Первоначальная настройка

1. Создайте `/opt/vff-fiscal`, `/opt/vff-fiscal/data` и `/opt/vff-fiscal/backups/releases`.
2. Разместите production `.env` в `/opt/vff-fiscal/.env` (mode `0600`).
3. Инициализируйте или скопируйте `state.json` в `/opt/vff-fiscal/data/state.json`
   (владелец UID/GID `65532`, mode `0600`).
4. Убедитесь, что Docker network `shm_default` существует и контейнеры SHM
   `shm-core-1` и `shm-spool-1` запущены.
5. Настройте SHM `client_token` так, чтобы он совпадал с `VFF_FISCAL_API_KEY`.
6. Скопируйте `ansible/hosts.ini.example` в `ansible/hosts.ini` локально.

## Политика версий

Развёртывания требуют явный 40-символьный commit SHA в нижнем регистре:

```bash
make deploy HOST=<inventory-host> VERSION=<40-char-sha>
```

После `git fetch` Ansible проверяет:

- объект commit существует
- checkout `HEAD` равен запрошенному SHA
- рабочее дерево на сервере чистое
- commit достижим из `origin/main`

Развёртывание commit, недостижимого из `origin/main`, требует явного override:

```bash
make deploy ... EXTRA='-e vff_fiscal_allow_unreachable_commit=true'
```

Имена веток, теги, плавающие refs и Make defaults из локального `HEAD` отклоняются.

### Одноразовый legacy image bootstrap

На старых production-хостах может работать `vff-fiscal:prod` — изменяемый тег,
который нельзя использовать как безопасную цель отката. Service role отклоняет
изменяемые previous images, если не запрошен явный одноразовый bootstrap.

Только для первого автоматизированного развёртывания сервиса укажите точный commit,
который породил текущий legacy image:

```bash
make deploy-service HOST=vff-fiscal VERSION=<new-40-char-sha> \
  EXTRA='-e vff_fiscal_allow_legacy_image_bootstrap=true -e vff_fiscal_legacy_image_commit=<legacy-40-char-sha>'
```

Во время bootstrap Ansible не pull'ит и не пересобирает legacy image. Он читает
image ID текущего контейнера, создаёт локальный immutable alias
`vff-fiscal:<first-12-chars-of-legacy-commit>`, указывающий на тот же image ID,
проверяет alias и записывает rollback Compose backup с immutable alias вместо `:prod`.
Backup metadata фиксирует `legacy_bootstrap=true`, исходный mutable tag, immutable alias,
явный legacy commit и image ID.

Никогда не предполагайте, что `vff-fiscal:prod` совпадает с checkout на диске. Если
legacy commit неверен, rollback metadata будет неверной. После появления `deploy-state.json`
bootstrap path запрещён; обычные развёртывания снова требуют commit-derived immutable tag
для previous running service image.

## Блокировка развёртывания

Мутирующие playbook'и (`deploy`, `deploy-service`, `deploy-adapter`,
`rollback-service` и `rollback-adapter`) захватывают одну host-side atomic lock directory
в `/opt/vff-fiscal/deploy.lock`. Read-only status playbook блокировку не использует.

Metadata блокировки содержит только безопасные операционные данные: тип операции,
timestamp, hostname контроллера, PID и случайный ownership token. Вторая мутирующая
операция немедленно завершается с ошибкой и существующими metadata блокировки, а не
ожидает. Блокировка снимается в `always` path и только при совпадении token, чтобы одна
операция не удалила lock другой активной операции.

Если оператор убедился, что процесс развёртывания или отката не выполняется и lock
устарел, изучите `/opt/vff-fiscal/deploy.lock/metadata.json`, затем вручную удалите
`/opt/vff-fiscal/deploy.lock`. Не удаляйте lock, пока активен процесс Ansible.

## Make targets

| Target | Назначение |
|--------|------------|
| `verify` | Syntax check, ansible-lint, Go tests |
| `deploy` | Сервис, затем адаптер, затем status |
| `deploy-service` | Только сервис vff-fiscal |
| `deploy-adapter` | Только SHM-адаптер |
| `deploy-status` | Read-only безопасный status report |
| `rollback-service` | Ручной откат сервиса из backup dir |
| `rollback-adapter` | Ручной откат адаптера из backup dir |

Требования:

- `HOST` обязателен для всех команд развёртывания и отката
- `VERSION` обязателен для deploy-команд
- `ansible/hosts.ini` должен существовать (Make завершится с понятной ошибкой, если его нет)
- Откат требует `ROLLBACK_CONFIRM=1`

Примеры:

```bash
make deploy HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-service HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-adapter HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-status HOST=vff-fiscal
make rollback-service HOST=vff-fiscal BACKUP_DIR=/opt/vff-fiscal/backups/releases/<release>/service ROLLBACK_CONFIRM=1
make rollback-adapter HOST=vff-fiscal BACKUP_DIR=/opt/vff-fiscal/backups/releases/<release>/adapter ROLLBACK_CONFIRM=1
```

Дополнительные аргументы Ansible передаются через `ANSIBLE_FLAGS` или `EXTRA`.

По умолчанию проверяется чистота локального контроллера. Исключительный override:

```bash
make deploy VFF_FISCAL_ALLOW_DIRTY_LOCAL_CONTROLLER=1 ...
```

Это **не** обходит server-side SHA, reachability или clean-checkout checks.

## Точный порядок развёртывания

### Комбинированное развёртывание (`make deploy`)

1. Common preflight (SHA validation, git checkout, host probes)
2. Service role
3. Adapter role
4. Status role (read-only verification)

### Порядок развёртывания сервиса

1. Проверка ключей `.env` и структуры `state.json` (без блокировки на `creating`)
2. Сборка image `vff-fiscal:<12-char-sha>`, если отсутствует или revision label не совпадает
3. Проверка, что image существует локально и несёт OCI label `org.opencontainers.image.revision`
4. Render и validation Compose candidate (`docker compose config -q`, без секции `build:`)
5. Ожидание через `docker top`, пока нет активного процесса `srv_customlab_nalog.cgi`
6. Определение, был ли spool уже на pause; pause только если эта операция владеет pause
7. Подтверждение pause и немедленное повторение `docker top` (работает и на pause)
8. Если процесс появился в race window, unpause только operation-owned pause и повтор
   полного gate ограниченное число раз
9. Повторная проверка `state.json` на чеки со статусом `creating`
10. Backup `state.json`, Compose и manifest в timestamped release directory (mode `0600`)
11. Атомарная замена Compose и `docker compose up -d --no-deps --pull never vff-fiscal`
12. Health check `/healthz` и authenticated smoke `/v1/user`
13. Атомарная запись `/opt/vff-fiscal/deploy-state.json` только при успехе
14. Unpause только если эта операция ставила spool на pause; проверка `State.Paused=false`

Идемпотентное повторное развёртывание того же running image пропускает шаги 5–10
(без pause spool и без backup).

### Порядок развёртывания адаптера

1. Staging CGI и helper modules в `/opt/shm/pay_systems/.vff-fiscal-stage/<sha>/`
2. Компиляция staged files в `shm-core-1` и `shm-spool-1`
3. Запись staged и active checksums; проверка immutable protection, safe SHM status
   и no-action diagnostic response
4. Решение `adapter_cutover_required`; cutover block пропускается, если всё уже совпадает
5. **Идемпотентный path:** явное принятие развёртывания, пропуск pause spool, backup,
   изменений `chattr` и замены live files; manifest обновляется до запрошенного commit,
   сохраняя существующий `backup_directory` от последнего реального cutover
6. **Cutover path:**
   1. Захват bounded `docker top` quiet/pause/post-pause gate
   2. Отказ, если spool уже был на pause оператором
   3. Backup live CGI, helper modules, ownership/modes/immutable metadata и safe SHM status
   4. Снятие `chattr +i`, проверка `lsattr` на `immutable=0`, установка только
      `adapter_immutable_removed=true`
   5. Очистка `need_update_to` через `shm-config.pl`, проверка, что marker отсутствует
   6. Установка `adapter_files_modification_started=true` непосредственно перед первой
      заменой active file
   7. Атомарная установка helper modules и CGI
   8. Компиляция installed files в `shm-core-1`, восстановление `enabled`, если не
      переопределено, и проверка active checksums против staged source
   9. Core diagnostic smoke test, пока spool остаётся на pause
   10. В `always`: восстановление `chattr +i`, проверка immutable protection и unpause
       только operation-owned pause
   11. Установка `adapter_post_unpause_validation_required=true` после успешного cutover
   12. Post-unpause validation (стабильный gate, не привязанный к mutable spool ownership
       facts): компиляция в running spool, missing-payment smoke, затем authenticated
       smoke `/v1/user` через `shm-auth-smoke.pl` (который инициализирует SHM перед
       `get_service`)
   13. `adapter_deployment_accepted=true` только после успешной post-unpause validation

Ни в один момент `shm-spool-1` не должен работать, пока live CGI не имеет `chattr +i`.
Adapter deploy и rollback отказываются изменять live files, если gate сообщает, что spool
уже был на pause оператором; operator paused state сохраняется, SHM config, `chattr`,
замена files и success manifest не записываются.

Ошибки конфигурации до первой замены active file не запускают transactional file
restoration; immutable protection восстанавливается, play завершается с явным отказом.
Ошибки во время или после замены files используют полный rescue path.

Если post-unpause validation завершается ошибкой, rescue устанавливает
`adapter_post_unpause_validation_failed=true`, восстанавливает previous CGI и оба helper
через `restore_adapter.yml` и завершает play с ошибкой. Restoration не может привести к
`deployment_status=success`.

### Принятие success manifest

Success manifest записывается только при `adapter_deployment_accepted=true`.

Допустимые path:

- **Идемпотентное развёртывание адаптера:** active files, immutable protection,
  `need_update_to` и diagnostics уже совпадают со staged content
- **Успешный cutover:** core validation, immutable restoration, spool unpause, spool Perl
  checks, missing-payment smoke и authenticated user smoke проходят успешно

Manifest **не** готовится и **не** записывается, если истинно любое из:

- `adapter_cutover_failed`
- `adapter_post_unpause_validation_failed`
- `adapter_restoration_failed`

Fail-closed guard также останавливает play перед подготовкой manifest, если развёртывание
не было явно принято.

Идемпотентное повторное развёртывание сервиса сохраняет immutable deployment history в
`deploy-state.json` и восстанавливает повреждённую history из trusted backup metadata
только при прохождении всех trust checks.

### Fail-closed обработка immutable

Rescue и `always` blocks при необходимости восстанавливают previous CGI и helper modules,
выполняют `chattr +i`, проверяют immutable flag через `lsattr` и unpause spool только
после подтверждения `+i`.

Если immutable protection нельзя проверить, play завершается явным отказом, и
**`shm-spool-1` намеренно остаётся на pause**. Сообщение об ошибке содержит только
безопасные команды восстановления:

```text
chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi
docker unpause shm-spool-1
```

Проверьте SHA256 CGI и выполните `perl -c` для CGI и helper modules перед ручным unpause.

SHM Perl helpers (`shm-config.pl`, `shm-auth-smoke.pl`) запускаются с host через
`docker exec -i ... perl - < script` и не требуют mount `/opt/vff-fiscal` внутри
`shm-core-1`. Оба helper инициализируют SHM context через
`SHM->new(skip_check_auth => 1)` перед вызовом `get_service`.

## Откат сервиса

Ручной откат сервиса:

- Требует `ROLLBACK_CONFIRM=1` и service backup directory
- Проверяет, что rollback image существует локально (без rebuild и pull)
- Проверяет backup Compose как candidate перед изменением production
- Определяет image сервиса `vff-fiscal` из candidate Compose и требует точного совпадения
  с backup metadata; если metadata содержит image ID, local image ID тоже должен совпасть
- Захватывает spool gate и блокируется на чеках со статусом `creating`
- Создаёт новый rollback-operation backup текущего state, Compose и manifest
- Восстанавливает backed-up Compose и выполняет `docker compose up -d --no-deps --pull never`
- Проверяет `/healthz` и authenticated `/v1/user`
- Восстанавливает и проверяет pre-rollback service, если rollback validation завершилась ошибкой
- **Никогда не восстанавливает `state.json` автоматически**
- Никогда не снимает pause со spool, который оператор поставил заранее

## Откат адаптера

Ручной откат адаптера по умолчанию использует `enabled=false`, если явно не передано
`vff_fiscal_rollback_adapter_enabled=true`.

Причина: backup может содержать previous third-party Customlab adapter; включение может
отправить FNS credentials и payment data на старый endpoint.

Rollback восстанавливает CGI, оба helper module, ownership, modes и immutable state.
Он очищает `need_update_to` и не восстанавливает его из backup.
Перед заменой создаётся fresh safety backup активного адаптера. И manual rollback, и
automatic deployment rescue используют одну staged, checksum-verified restoration
transaction. Если manual rollback завершается ошибкой, safety backup восстанавливается
до fail play.

```bash
make rollback-adapter HOST=vff-fiscal BACKUP_DIR=... ROLLBACK_CONFIRM=1
# явное включение (опасно):
make rollback-adapter ... EXTRA='-e vff_fiscal_rollback_adapter_enabled=true'
```

Порядок restoration transaction в `restore_adapter.yml` совпадает с normal cutover:
после проверки `chattr` устанавливается только `adapter_immutable_removed=true`,
затем очищается `need_update_to`, и только перед первой заменой helper/CGI
устанавливается `adapter_files_modification_started=true`.

## Политика backup для state.json

- Проверяется без вывода secret values (version, auth keys present, receipts object)
- Backup выполняется только после pause spool и успешной post-pause проверки `creating`
- Backup хранятся в `/opt/vff-fiscal/backups/releases/<timestamp>-<sha>/{service,adapter}/`
- Backup files имеют mode `0600` или строже и никогда не коммитятся в Git
- Rescue/rollback никогда не восстанавливает `state.json` автоматически

## Поведение immutable CGI

Live adapter CGI защищён `chattr +i` вне короткого окна замены.
Ansible снимает `+i` только пока spool на pause, переустанавливает files, перекомпилирует,
затем восстанавливает `+i`.

### Восстановление, если immutable flag отсутствует

```bash
docker pause shm-spool-1
chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi
lsattr /opt/shm/pay_systems/srv_customlab_nalog.cgi
docker unpause shm-spool-1
```

### Восстановление при зависшем paused spool

```bash
lsattr /opt/shm/pay_systems/srv_customlab_nalog.cgi   # confirm +i present
docker unpause shm-spool-1
docker inspect -f '{{.State.Paused}}' shm-spool-1
```

Если развёртывание прервалось mid-cutover, изучите `/opt/vff-fiscal/backups/releases/`
и `deploy-state.json` перед ручными действиями.

## Production Compose image model

Rendered Compose использует только immutable tag:

```yaml
image: vff-fiscal:<12-char-sha>
```

После render секции `build:` отсутствуют. Cutover использует `--no-deps`, чтобы контейнеры
SHM никогда не пересоздавались.

## Check mode

`ansible-playbook --check` выполняет read-only preflight, где возможно. Он **не**:

- делает checkout Git revisions
- собирает images
- ставит containers на pause
- изменяет SHM configuration или флаги `chattr`
- копирует production files
- пересоздаёт containers
- записывает manifests

Runtime Docker/SHM validation всё равно требует live host check вне check mode.
Пропущенные mutating tasks инициализируют transaction facts, поэтому check mode не
зависит от результатов checkout, build, staging, pause или manifest tasks.

## Семантика backup metadata

Service backup metadata записывает `target_commit`, `target_image`,
`previous_commit`, `previous_image`, `previous_image_id` и наличие previous Compose.
Adapter backup metadata записывает target и previous commits, все три previous file
checksums, previous enabled state и previous immutable state. Если previous version
старше `deploy-state.json`, commit записывается как `unknown-pre-manifest`, а не
выводится из нового target.

## Поведение при ошибках (summary)

| Точка отказа | Результат |
|--------------|-----------|
| git fetch / unreachable SHA | Play fails; cutover не выполняется |
| image build failure | Play fails; spool не ставится на pause |
| Compose validation failure | Play fails до pause |
| spool cannot pause | Play fails; prior state сохраняется |
| active CGI never finishes | Bounded wait, затем безопасный fail |
| `creating` receipt after pause | Play fails; spool unpaused в `always` |
| state backup failure | Cutover прерван |
| health / auth smoke failure | Service rescue восстанавливает previous Compose/image; state не восстанавливается |
| adapter install/compile failure | Adapter rescue восстанавливает backed-up CGI + helpers, если file modification started; `need_update_to` cleared |
| adapter post-unpause validation failure | Previous CGI и helpers восстановлены; play fails; success manifest не записывается |
| `chattr +i` failure | Spool остаётся на pause, пока `+i` не проверен или не выполнено ручное восстановление |
| manifest write failure | Развёртывание помечено failed; prior manifest сохранён |

## Конфигурация токена SHM-адаптера

Приоритет:

1. непустой `client_token` из конфигурации модуля SHM
2. непустой `VFF_FISCAL_API_TOKEN` в контейнерах SHM
3. контролируемая ошибка конфигурации

## Метка времени платежа

Адаптер передаёт `comment.object.captured_at` без изменений как `operation_time`.

## Контракт приватности SHM-адаптера

Успешный stdout CGI при создании чека содержит только `status` и `msg`.
UUID чека, print URL, JSON URL и фискальные идентификаторы в этих URL хранятся в
metadata платежа SHM (`comment.receiptUuid`, `receiptLink`, `receiptJsonLink`) и не
должны появляться в adapter output, который сохраняет spool.

## Секреты

Никогда не коммитьте `ansible/hosts.ini`, `.env`, `data/state.json`, backups или tokens.

Ansible tasks, которые могут раскрыть секреты, используют `no_log: true`. Smoke tests
выводят только HTTP status, safe counters и поля вроде `token_present=true`.

## Известные ограничения

- Check mode не может полностью проверить runtime поведение Docker/SHM.
- Adapter rollback по умолчанию отключает adapter, даже если backup был enabled.
- Идемпотентное развёртывание пропускает backups, когда target image уже healthy.
- Reachability override должен быть явным и именованным.

## Локальная проверка

```bash
make verify
python3 -m unittest discover -s tests/deploy -v
make ansible-syntax
make ansible-lint
```
