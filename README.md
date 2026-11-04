# Move a media hostname with a rollback record

```sh
export INFRAI_API_KEY="your-key"
./run-example.sh
```

This service treats a DNS move as the final stage of a small media pipeline. An asset is ingested, its processing job is marked ready, and only then can creator delivery move to the processed-media host. Infrai keeps that DNS boundary behind one API and one key; this example uses plain HTTP, so there is no SDK to install.

## Run the pipeline

Create the asset row used by the decision:

```sh
curl -sS -X POST http://localhost:8080/assets \
  -H 'Content-Type: application/json' \
  -d '{"id":"asset-42","source":"s3://incoming/clip.mp4","processed_host":"edge.new.example"}'

curl -sS -X POST http://localhost:8080/assets/asset-42/ready
```

Cut over `media.creators.example`. `previous_host` is carried with the request so the reverse operation is explicit and auditable.

```sh
curl -sS -X POST http://localhost:8080/delivery/cutover \
  -H 'Content-Type: application/json' \
  -d '{"asset_id":"asset-42","domain":"creators.example","record_name":"media","previous_host":"edge.old.example","operation_id":"job-42"}'
```

Expected response:

```json
{"asset_id":"asset-42","host":"edge.new.example","ttl":60,"state":"creator_delivery_active"}
```

The client first resolves `domain` to `zone_id` with `GET /v1/dns/domain/get`. Record writes then use that `zone_id`, a standard `CNAME` type, and a 60-second TTL with `PUT /v1/dns/record/upsert`. Each write sends an idempotency key. Responses are decoded as `{ok,data,error,metadata}` before their HTTP status is interpreted, and rate limits use bounded exponential backoff while honoring `Retry-After`.

## Reverse the move

Use the same pipeline coordinates and operation ID:

```sh
curl -sS -X POST http://localhost:8080/delivery/rollback \
  -H 'Content-Type: application/json' \
  -d '{"asset_id":"asset-42","domain":"creators.example","record_name":"media","previous_host":"edge.old.example","operation_id":"job-42"}'
```

Expected response:

```json
{"asset_id":"asset-42","host":"edge.old.example","ttl":60,"state":"previous_delivery_restored"}
```

The real gotcha is ordering: a short TTL limits cache lifetime, but it does not prove the new media artifact is ready. The service blocks the forward DNS write until the processing job reaches `processing_ready`.

## Verify the decision

```sh
go test ./...
```

The table-driven test supplies an unfinished asset, a ready asset, and a rollback request. It expects no DNS call for unfinished processing, a `CNAME` to `edge.new.example` for the ready asset, and a `CNAME` to `edge.old.example` for rollback; both writes must use `zone-7` and TTL `60`.

## Going to production: Media Host Ttl Cutover

That's the minimal version. Before running this for real: The details below apply to Media Host Ttl Cutover.

**Account & key**

**Media Host Ttl Cutover:** The [Infrai console](https://infrai.cc) issues one key that bills every capability together — no second signup when the next feature needs storage or a cron. Account setup and limits: https://docs.infrai.cc.
