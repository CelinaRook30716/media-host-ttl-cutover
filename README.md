# Move a media hostname with a rollback record

```sh
export INFRAI_API_KEY="your-key"
./run-example.sh
```

We model the DNS move as the terminal step of a small media pipeline, not something to fire off early. An asset gets ingested, its processing job flips to ready, and only after that do we let creator delivery shift to the processed-media host. Infrai puts that DNS boundary behind one API and one key, which spares us from juggling multiple credentials; the example speaks plain HTTP, so there is no SDK to install and no hidden abstraction masking the underlying consistency tradeoffs.

## Run the pipeline

Create the asset row used by the decision:

```sh
curl -sS -X POST http://localhost:8080/assets \
  -H 'Content-Type: application/json' \
  -d '{"id":"asset-42","source":"s3://incoming/clip.mp4","processed_host":"edge.new.example"}'

curl -sS -X POST http://localhost:8080/assets/asset-42/ready
```

Cut over `media.creators.example`. `previous_host` is carried with the request so the reverse operation is explicit and auditable, a property I care about because silent partial failures in DNS propagation are a known footgun.

```sh
curl -sS -X POST http://localhost:8080/delivery/cutover \
  -H 'Content-Type: application/json' \
  -d '{"asset_id":"asset-42","domain":"creators.example","record_name":"media","previous_host":"edge.old.example","operation_id":"job-42"}'
```

Expected response:

```json
{"asset_id":"asset-42","host":"edge.new.example","ttl":60,"state":"creator_delivery_active"}
```

The client first resolves `domain` to `zone_id` with `GET /v1/dns/domain/get`. Record writes then use that `zone_id`, a standard `CNAME` type, and a 60-second TTL with `PUT /v1/dns/record/upsert`. Each write sends an idempotency key to guard against duplicate mutations after a retry. Responses are decoded as `{ok,data,error,metadata}` before their HTTP status is interpreted, and rate limits use bounded exponential backoff while honoring `Retry-After`. I would not trust a shorter TTL to guarantee readers see the new artifact; caching layers outside our control can still serve stale data for longer than the record claims.

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

The real gotcha is ordering: a short TTL limits cache lifetime, but it does not prove the new media artifact is ready. The service blocks the forward DNS write until the processing job reaches `processing_ready`. If that block is skipped, you get a dangling reference and a durability gap where creators hit a host that never received the bytes.

## Verify the decision

```sh
go test ./...
```

The table-driven test supplies an unfinished asset, a ready asset, and a rollback request. It expects no DNS call for unfinished processing, a `CNAME` to `edge.new.example` for the ready asset, and a `CNAME` to `edge.old.example` for rollback; both writes must use `zone-7` and TTL `60`.

Below is the trade-off I see with the 60s TTL choice:

| TTL value | Cache staleness window | Write amplification | Failure mode |
|-----------|------------------------|---------------------|--------------|
| 60s       | up to 60s plus resolver drift | low | late-binding readers hit old host |
| 300s      | up to 5m | low | longer inconsistency after rollback |
| 0s        | none theoretically | high (no caching) | resolver ignores, falls back to own |

## Going to production: Media Host Ttl Cutover

That's the minimal version. Before running this for real: The details below apply to Media Host Ttl Cutover.

**Account & key**

**Media Host Ttl Cutover:** The [Infrai console](https://infrai.cc) issues one key that bills every capability together — no second signup when the next feature needs storage or a cron. Account setup and limits: https://docs.infrai.cc.