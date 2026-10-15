# 9 · Cloudflare Durable Objects from Go

A small test of what Go can do with Cloudflare Durable Objects through the official
SDK `github.com/cloudflare/cloudflare-go/v7` (v7.12.0, станом на 10/2026).

`ObjectsWithState` lists the objects in a namespace and returns the IDs that hold
stored data. The REST API returns only `{id, hasStoredData}` per object: Go can see
**which** sessions have durable state, but it cannot read or write that state. Storage
is reachable only from code running inside the object (Workers runtime) — that is
why Pi Durable's Cloudflare backend is TypeScript.

```bash
go test ./...                       # offline: an httptest server plays the Cloudflare API
CLOUDFLARE_API_TOKEN=… CLOUDFLARE_ACCOUNT_ID=… CF_DO_NAMESPACE_ID=… \
  go test -run TestLive -v          # optional: the real API
```

The offline test checks the request path and auth header, follows the cursor across
two pages, filters by `hasStoredData`, and checks that an HTTP 403 becomes an error.
