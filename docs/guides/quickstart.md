# Quickstart

Start Triage with a fake provider, then switch to the learned `bert` strategy.

## Step 1: Triage with a fake provider

Run the commands in the [README](../../README.md#quickstart). They use `fakellm` and the
`length` strategy. No downloads and no API keys. The response carries `X-Triage-Decision`,
`X-Triage-Route` and `X-Triage-Target` headers.

## Step 2: the learned `bert` strategy

Run from the repository root:

<!-- quickstart-bert:start -->
```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.bert.yml run --rm --no-deps triage models pull bert
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.bert.yml up -d --wait
curl -si localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"triage-auto","messages":[{"role":"user","content":"Prove that there are infinitely many primes."}]}'
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.bert.yml down
```
<!-- quickstart-bert:end -->

1. `models pull bert` downloads the model (about 1.1 GB) into the `triage-models` volume.
   A second pull downloads nothing.
2. `up` restarts Triage with
   [`deploy/config.quickstart-bert.yaml`](../../deploy/config.quickstart-bert.yaml)
   (`strategy: bert`).
3. The `X-Triage-Route-Reason` header shows the score, for example
   `strategy:bert p=0.42<0.50`. The full decision is at
   `localhost:9091/v1/routing-decisions/<X-Triage-Decision>`.

To run a local build instead of the released images, run `make docker` and set
`RAYLD_TRIAGE_IMAGE=thnhdatle/triage:dev` and `RAYLD_FAKELLM_IMAGE=thnhdatle/fakellm:dev`.

`down` keeps the model. `down -v` also deletes the `triage-models` volume.

Next: [models.md](models.md) for the INT8 build, [routing-strategies.md](routing-strategies.md)
for choosing a threshold.
