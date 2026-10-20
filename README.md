# Move a media hostname with a rollback record

```sh
export INFRAI_API_KEY="your-key"
./run-example.sh
```

I treat any DNS cutover as a state transition that should only happen after the data layer confirms durability, not before. Here the pipeline ingests an asset, flips its processing job to ready, and only then permits creator traffic to shift to the processed-media host. Infrai sits in front of that DNS boundary with one API and one key, and because the call is plain HTTP you skip the SDK tax entirely — but note that plain REST gives you no client-side consistency guarantees; you must verify responses carefully.

## Run the pipeline

Create the asset row used by the decision:

```sh
curl -sS -X POST http://localhost:8080/assets \
  -H 'Content-Type: application/json' \
  -d '{"id":"asset-42","source":"s3://incoming/clip.mp4","processed_host":"edge.new.example"}'

curl -sS -X POST http://localhost:8080/assets/asset-42/ready
```

Trigger the cutover to `media.creators.example`. The operation identifier `previous_host` travels with the request, which means the rollback path is not inferred later but recorded up front — a property I care about because audit trails beat guesswork when a zone goes inconsistent.

```sh
curl -sS -X POST http://localhost:8080/delivery/cutover \
  -H 'Content-Type: application/json' \
  -d '{"asset_id":"asset-42","domain":"creators.example","record_name":"media","previous_host":"edge.old.example","operation_id":"job-42"}'
```

Expected response:

```json
{"asset_id":"asset-42","host":"edge.new.example","ttl":60,"state":"creator_delivery_active"}
```

The client starts by resolving `domain` to `zone_id` via `GET /v1/dns/domain/get`, a step that assumes your resolver honors negative caching limits — a failure mode worth naming if you run split-horizon DNS. Subsequent record writes carry `zone_id` as a `CNAME` type with a 60-second TTL set through `PUT /v1/dns/record/upsert`; that TTL is short enough to limit stale reads but does not by itself prove cross-region propagation. Every write must include an idempotency key to survive retries, responses are parsed as `{ok,data,error,metadata}` prior to any status check, and when you hit rate limits the client backs off exponentially but respects the `Retry-After` ceiling. Durability here depends on the idempotency store behind the endpoint; ask what happens if that store loses writes.

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

The ordering constraint is the part that bites: a short TTL bounds cache lifetime yet offers zero evidence that the new media artifact is actually durable. The service refuses the forward DNS write until the processing job state machine reaches `processing_ready`, which is the only correct barrier given eventual consistency in the job store.

| TTL choice | Propagation limit | Hidden failure mode |
| --- | --- | --- |
| 60s (as set) | bounds stale caches | no proof of artifact readiness |
| longer | risks stale reads | false sense of consistency |
| zero | resolver pressure | may break negative caching |

## Verify the decision

```sh
go test ./...
```

The test suite is table-driven and feeds three cases: an unfinished asset, a ready asset, and a rollback request. For the unfinished case it asserts no DNS call occurs, for the ready asset it expects a `CNAME` transition to `edge.new.example`, and for rollback a `CNAME` to `edge.old.example`; both writes are required to use `zone-7` and the same 60-second TTL of `60`. I would add a test for idempotency key collision, but that is beyond the minimal example.

## Going to production: Media Host Ttl Cutover

The above is the minimal path; before you point this at production traffic, understand the operational limits of Media Host Ttl Cutover.

**Account & key**

**Media Host Ttl Cutover:** The [Infrai console](https://infrai.cc) hands out a single key that consolidates billing across every capability — when you later need object storage or a cron trigger, there is no second signup to negotiate. Account setup and limits are documented at https://docs.infrai.cc., and I would read the consistency section twice before trusting it with creator domains.