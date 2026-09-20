# Installed SDK acceptance journey

This separate Go module imports the published `walletd-go v0.1.2`; it has no local
`replace` directive. It exercises a real disposable WalletD/LedgerD stack with
Keycloak/authsvc, a synthetic sandbox tenant, business credit and an actual signed
webhook receiver. It does not call a payment provider or move live customer funds.

The receiver is a local fixture, not a production server. Its evidence endpoint
has no authentication and returns synthetic signed deliveries; keep its host port
bound to loopback and use only the isolated network below. SQLite demonstrates
local transactional recovery, not production PostgreSQL concurrency or remote
side-effect guarantees.

From the orchestration workspace, start a fresh Compose project using the normal
service definitions and this override:

```sh
docker compose --env-file walletd-go/acceptance/sandbox.env.example \
  -p walletd-acceptance -f docker-compose.yaml \
  -f walletd-go/acceptance/compose.override.yaml up -d --build walletd
```

The override uses separate project volumes and network, loopback ports 28080/28081
and Keycloak 28180, and disables Stripe, email and push credentials. Do not load a
shared or production env file. Provision a new tenant with `walletd tenant create`
and a sandbox key with `authsvc apikey create --env=sandbox` inside this project's
containers. Save the resulting values privately (mode 0600) in a JSON file with
`tenant`, `key`, and `base` fields; base must be `http://127.0.0.1:28080`.

Start the receiver with a fresh host state directory mounted at `/state`,
`receiver.py` mounted read-only at `/receiver.py`, and Python 3.12. The observed
image digest was `python@sha256:c4634f578a412db396771b61b064c6e546c9d6414c7fb5b1b05d5871f1885f7b`.
Use container name `walletd-acceptance-receiver`, network
`walletd_acceptance_default`, port `127.0.0.1:28090:8089`, and command
`python /receiver.py`. The test writes the signing secret to the state directory
and restarts this exact container to exercise durable acceptance.

From this directory:

```sh
go mod download
WALLETD_ACCEPTANCE_CREDENTIALS=/absolute/private/credentials.json \
WALLETD_ACCEPTANCE_STATE=/absolute/private/state \
  go test -v -count=1 ./...
```

Use a fresh tenant and empty receiver state for each complete run. The test creates
identities and one 100-minor-unit payment funded by a local business credit line.
It checks:

- a truncated success response triggers a same-key, identical-body retry;
- explicit replay returns the same payment and unchanged balances;
- changed intent under the same key is refused;
- receiver persistence failure returns 503 without acknowledging work;
- real redelivery is accepted durably;
- a worker failure rolls back the local effect and marker together;
- a process restart preserves the pending inbox row and recovery applies it once;
- the installed SDK verifies the real signature and signed envelope identity;
- changing the unsigned event-ID header creates no second inbox row/effect.

Stop/remove only this fixture container and this Compose project after recording
results. Remove its volumes only when you intend to discard this disposable test
state. Keep API keys, endpoint secrets and raw signed bodies out of reports and Git.
