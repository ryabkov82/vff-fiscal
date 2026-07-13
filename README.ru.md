# vff-fiscal

[English version](README.md)

Self-hosted Go-сервис для выпуска чеков через неофициальный web API `lknpd.nalog.ru` («Мой налог») и интеграции с SHM.

> Это неофициальная интеграция. Внутренний web API ФНС может измениться без предупреждения. Проверяйте каждое обновление перед использованием в production.

## Архитектура

```text
Успешный платёж в SHM
  -> аудируемый Perl-адаптер (внутри shm-core/shm-spool)
  -> POST /v1/receipts по приватной Docker-сети
  -> vff-fiscal
  -> POST /api/v1/auth/token при необходимости обновления access token
  -> POST /api/v1/income
  -> approvedReceiptUuid + URL чека
  -> адаптер сохраняет receiptUuid/receiptLink/receiptJsonLink в comment платежа SHM
  -> успешный stdout CGI содержит только status и msg (без идентификаторов чека)
```

Refresh token ФНС, device ID и ИНН остаются внутри `vff-fiscal`; SHM не передаёт их третьим сторонам.

## Реализованный API

- `GET /healthz` — проверка liveness, без аутентификации.
- `GET /v1/user` — read-only проверка аутентификации в ФНС.
- `POST /v1/receipts` — создание одного чека.
- `GET /v1/receipts/{external_id}` — локально сохранённое состояние.
- `POST /v1/receipts/{external_id}/cancel` — отмена созданного чека.

Все endpoint'ы `/v1/*` требуют `Authorization: Bearer <VFF_FISCAL_API_KEY>`.

## Свойства безопасности

- Ротация refresh token сохраняется атомарно с правами `0600`.
- Запросы идемпотентны по `external_id`.
- Сетевой таймаут во время `POST /income` переводит состояние в `unknown`; сервис не повторяет слепо операцию, которая могла уже завершиться успешно.
- Секреты и тела запросов не пишутся в логи.
- Сервис должен быть доступен только через приватную Docker-сеть SHM.

Текущее файловое хранилище состояния подходит для одного экземпляра сервиса. Не запускайте несколько реплик на одном файле состояния.

## Первоначальная настройка

```bash
cp .env.example .env
mkdir -p data
chmod 700 data
openssl rand -hex 32
```

Поместите сгенерированный ключ в `VFF_FISCAL_API_KEY`. Заполните:

- `LKNPD_INN`
- `LKNPD_REFRESH_TOKEN`
- `LKNPD_DEVICE_ID`

Refresh token и device ID должны принадлежать одной браузерной сессии/устройству.

Запуск сервиса:

```bash
docker compose -f deploy/docker-compose.example.yml up -d --build
```

## Сначала read-only проверка

```bash
set -a
. ./.env
set +a

curl -fsS \
  -H "Authorization: Bearer $VFF_FISCAL_API_KEY" \
  http://127.0.0.1:18080/v1/user | jq
```

Это обновляет access token и вызывает `GET /api/v1/user`; чек не создаётся.

## Создание чека вручную

Только после успешной read-only проверки:

```bash
curl -fsS \
  -H "Authorization: Bearer $VFF_FISCAL_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "external_id": "manual:test-001",
    "amount": "1.00",
    "service_name": "Тестовая услуга",
    "operation_time": "2026-07-09T19:30:00+03:00"
  }' \
  http://127.0.0.1:18080/v1/receipts | jq
```

Используйте реальную транзакцию и сумму, которую нужно зарегистрировать законно. **Не создавайте произвольные production-чеки только для проверки ПО.**

## SHM-адаптер

Текущее ядро SHM вызывает файл `srv_customlab_nalog.cgi`, поэтому проект предоставляет читаемую drop-in замену:

```text
adapters/shm/srv_customlab_nalog.cgi
```

Перед заменой:

```bash
cp -a /opt/shm/pay_systems/srv_customlab_nalog.cgi \
  /opt/shm/pay_systems/srv_customlab_nalog.cgi.vendor-backup

install -o www-data -g www-data -m 0755 \
  adapters/shm/srv_customlab_nalog.cgi \
  /opt/shm/pay_systems/srv_customlab_nalog.cgi
```

Настройте API-ключ адаптера в форме модуля SHM как **Client Token** (`client_token`). Его значение должно совпадать с `VFF_FISCAL_API_KEY` в vff-fiscal. Если `client_token` задан через UI SHM, пересоздание контейнеров SHM не требуется.

`VFF_FISCAL_API_TOKEN` остаётся опциональным fallback для развёртываний только через переменные окружения. Добавляйте его в `shm-core-1` и `shm-spool-1` только если не используете `client_token`:

```text
VFF_FISCAL_API_TOKEN=<то же значение, что и VFF_FISCAL_API_KEY>
```

Приоритет токена в адаптере:

1. непустой `client_token` из конфигурации модуля SHM
2. непустая переменная окружения `VFF_FISCAL_API_TOKEN`
3. контролируемая ошибка конфигурации

Существующая форма настроек SHM по-прежнему управляет:

- Enabled
- Client Token (`client_token`)
- Service name
- Payment systems
- `backend_url`, если добавлен напрямую в конфигурацию

Старые поля Customlab для INN, Refresh Token и time zone новым адаптером не используются. `LKNPD_INN`, `LKNPD_REFRESH_TOKEN`, `LKNPD_DEVICE_ID` и `LKNPD_TIMEZONE_OFFSET` остаются только в конфигурации vff-fiscal.

Не коммитьте реальные секреты, токены или учётные данные в Git.

### Метка времени платежа

Авторитетная метка фискализации — `comment.object.captured_at` из объекта платежа YooKassa в SHM. Адаптер передаёт `captured_at` без изменений как `operation_time` в payload `POST /v1/receipts`. `created_at`, время создания записи SHM и время выполнения адаптера не используются.

vff-fiscal конвертирует RFC3339 instant в `LKNPD_TIMEZONE_OFFSET` перед отправкой в ФНС.

Развёртывайте CGI и весь каталог `lib/VFFFiscal/` вместе:

- `srv_customlab_nalog.cgi`
- `lib/VFFFiscal/PaymentTimestamp.pm`
- `lib/VFFFiscal/AdapterConfig.pm`

Не заменяйте production-адаптер, пока не пройдут проверки staging. Успешные ответы адаптера не должны раскрывать UUID чека, print URL или фискальные идентификаторы в stdout, который сохраняет spool; эти значения должны быть только в metadata платежа SHM.

Проверьте staging-копию в `/tmp` внутри контейнера SHM перед установкой:

```bash
docker exec shm-core-1 mkdir -p /tmp/vff-fiscal-adapter/lib/VFFFiscal

docker cp adapters/shm/srv_customlab_nalog.cgi \
  shm-core-1:/tmp/vff-fiscal-adapter/srv_customlab_nalog.cgi

docker cp adapters/shm/lib/VFFFiscal \
  shm-core-1:/tmp/vff-fiscal-adapter/lib/

docker exec shm-core-1 \
  perl -c /tmp/vff-fiscal-adapter/srv_customlab_nalog.cgi
```

Когда готовы к production, скопируйте CGI и каталог `lib/` рядом с ним:

```bash
install -d -o www-data -g www-data -m 0755 /opt/shm/pay_systems/lib
cp -a adapters/shm/lib/* /opt/shm/pay_systems/lib/
```

### Риск перезаписи при обновлении SHM

SHM Cloud может перезаписать `srv_customlab_nalog.cgi`. Держите ожидаемый SHA-256 под мониторингом или управляйте файлом через автоматизацию развёртывания после обновлений SHM.

## Состояния чека

- `creating` — локальный запрос сохранён до вызова ФНС.
- `created` — ФНС вернул `approvedReceiptUuid`.
- `failed` — ФНС вернул однозначную ошибку.
- `unknown` — сбой транспорта/сервера после того, как create-запрос мог дойти до ФНС; требуется ручная сверка.
- `cancelled` — отмена завершилась успешно.

## Разработка

Требуется Go 1.26 или новее.

```bash
make fmt
make test
make build
```

## Развёртывание

Production-развёртывание выполняется вручную через Ansible. См. [docs/DEPLOYMENT.ru.md](docs/DEPLOYMENT.ru.md) ([English](docs/DEPLOYMENT.md)).

Примеры:

```bash
cp ansible/hosts.ini.example ansible/hosts.ini
make verify
make deploy HOST=vff-fiscal VERSION=<40-char-sha>
make deploy-status HOST=vff-fiscal
```

Никогда не коммитьте `ansible/hosts.ini`, `.env` или `data/state.json`.
