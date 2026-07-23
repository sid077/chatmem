# chatmem telemetry ingest

Cloudflare Worker + D1 that receives anonymous pings from `chatmem` clients:
install id, version, per-window counters (captures/searches/gets/errors),
model + client distribution, latency percentiles. **Never message content.**

## Deploy (one-time)

```bash
cd server/telemetry-worker
npm install
npx wrangler login                                                    # once per machine
npx wrangler d1 create chatmem-telemetry                              # copy database_id
sed -i.bak "s/REPLACE_ME_AFTER_wrangler_d1_create/<DATABASE_ID>/" wrangler.toml && rm wrangler.toml.bak
npx wrangler d1 execute chatmem-telemetry --remote --file=schema.sql
npx wrangler deploy                                                   # prints the URL
```

Then point clients at the deployed URL:

```bash
export CHATMEM_TELEMETRY_URL="https://chatmem-ingest.<your-subdomain>.workers.dev"
```

Or bake it in globally by adding to `/etc/environment` / your shell profile.

## Inspect data

```bash
# Total pings, unique installs, last 24h captures
npx wrangler d1 execute chatmem-telemetry --remote --command="
  SELECT COUNT(*) AS pings,
         COUNT(DISTINCT install_id) AS installs,
         SUM(CASE WHEN received_at > datetime('now','-1 day') THEN captures ELSE 0 END) AS captures_24h
  FROM pings;
"

# Per-install 30-day rollup via the built-in view
npx wrangler d1 execute chatmem-telemetry --remote --command="SELECT * FROM install_summary ORDER BY total_captures DESC LIMIT 20;"

# Model distribution (JSON aggregate)
npx wrangler d1 execute chatmem-telemetry --remote --command="SELECT models FROM pings WHERE received_at > datetime('now','-7 days');"
```

## Local dev

```bash
npx wrangler dev                                    # local Worker, in-memory D1
curl -X POST http://127.0.0.1:8787/v1/ping \
  -H 'Content-Type: application/json' \
  -d '{"install_id":"test","version":"dev","events":{"captures":1}}'
```

## Contract

- `POST /v1/ping` with a JSON body matching `internal/telemetry.Payload` in the
  Go client. Any other method or path returns 4xx.
- 200 → accepted. 4xx → permanent failure (client drops). 5xx → transient,
  client retries with backoff and persists to `pending/*.json` on final failure.
- Max payload 32 KB. Empty install_id rejected.
- CORS enabled (`*`) so a future dashboard hosted anywhere can query the same
  Worker (read-side endpoints not implemented yet).

## Cost / limits

Cloudflare free tier caps: 100k Worker requests/day, 5 GB D1 storage,
25M D1 reads / 5M writes per day. A single active user emits ~288 pings/day
(5 min interval). 300 active users still fits comfortably in the free tier.
