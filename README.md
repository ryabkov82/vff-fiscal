# vff-fiscal

Self-hosted Go service for issuing receipts through the unofficial web API used by `lknpd.nalog.ru` ("Мой налог") and integrating it with SHM.

> This is an unofficial integration. The internal FNS web API can change without notice. Test every update before production use.

## Architecture

```text
SHM successful payment
  -> audited Perl adapter (inside shm-core/shm-spool)
  -> POST /v1/receipts over the private Docker network
  -> vff-fiscal
  -> POST /api/v1/auth/token when access token refresh is required
  -> POST /api/v1/income
  -> approvedReceiptUuid + receipt URLs
  -> adapter writes receiptUuid/receiptLink into SHM payment comment
```

The FNS refresh token, device ID and INN stay inside `vff-fiscal`; SHM never sends them to a third party.

## Implemented API

- `GET /healthz` — liveness check, no authentication.
- `GET /v1/user` — read-only FNS authentication test.
- `POST /v1/receipts` — create one receipt.
- `GET /v1/receipts/{external_id}` — return locally persisted state.
- `POST /v1/receipts/{external_id}/cancel` — cancel a created receipt.

Every `/v1/*` endpoint requires `Authorization: Bearer <VFF_FISCAL_API_KEY>`.

## Safety properties

- Refresh-token rotation is persisted atomically with mode `0600`.
- Requests are idempotent by `external_id`.
- A network timeout during `POST /income` produces state `unknown`; the service will not blindly repeat an operation that may already have succeeded.
- Secrets and request bodies are not written to logs.
- The service should only be reachable through the private SHM Docker network.

The current file-backed state store is appropriate for a single service instance. Do not run multiple replicas against one state file.

## Initial setup

```bash
cp .env.example .env
mkdir -p data
chmod 700 data
openssl rand -hex 32
```

Put the generated key into `VFF_FISCAL_API_KEY`. Fill in:

- `LKNPD_INN`
- `LKNPD_REFRESH_TOKEN`
- `LKNPD_DEVICE_ID`

The refresh token and device ID must belong to the same browser session/device.

Start the service:

```bash
docker compose -f deploy/docker-compose.example.yml up -d --build
```

## Read-only test first

```bash
set -a
. ./.env
set +a

curl -fsS \
  -H "Authorization: Bearer $VFF_FISCAL_API_KEY" \
  http://127.0.0.1:18080/v1/user | jq
```

This refreshes the access token and calls `GET /api/v1/user`; it does not create a receipt.

## Create a receipt manually

Only after the read-only test succeeds:

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

Use a real transaction and amount that should legally be registered. Do not create arbitrary production receipts merely to test software.

## SHM adapter

The current SHM core invokes a file named `srv_customlab_nalog.cgi`, so the project provides a drop-in, readable replacement at:

```text
adapters/shm/srv_customlab_nalog.cgi
```

Before replacement:

```bash
cp -a /opt/shm/pay_systems/srv_customlab_nalog.cgi \
  /opt/shm/pay_systems/srv_customlab_nalog.cgi.vendor-backup

install -o www-data -g www-data -m 0755 \
  adapters/shm/srv_customlab_nalog.cgi \
  /opt/shm/pay_systems/srv_customlab_nalog.cgi
```

Add the same API key to both `shm-core-1` and `shm-spool-1` as environment variable:

```text
VFF_FISCAL_API_TOKEN=<same value as VFF_FISCAL_API_KEY>
```

The existing SHM settings form can still control:

- Enabled
- Service name
- Payment systems
- `backend_url` if added directly to configuration

The fake Customlab refresh token is not used by the replacement adapter and should be removed from SHM configuration.

### Important update risk

SHM Cloud may overwrite `srv_customlab_nalog.cgi`. Keep its expected SHA-256 under monitoring or manage the file through deployment automation after SHM updates.

## Receipt states

- `creating` — local request persisted before calling FNS.
- `created` — FNS returned `approvedReceiptUuid`.
- `failed` — FNS returned a definite error.
- `unknown` — transport/server failure after the create request may have reached FNS; manual reconciliation is required.
- `cancelled` — cancellation returned successfully.

## Development

Requires Go 1.26 or newer.

```bash
make fmt
make test
make build
```
