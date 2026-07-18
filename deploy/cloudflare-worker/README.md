# R2 Public Access Policy Worker

This Cloudflare Worker is one layer of a **defense-in-depth** strategy to prevent
public exposure of database backups (`backups/` prefix) when a shared R2 bucket
is exposed via a public domain (Public Base URL).

> ⚠️ **The Worker alone is NOT sufficient.** A bucket can be exposed through
> multiple public entry points (r2.dev, other custom domains) that bypass the
> Worker. The Worker only protects the domains listed in its `routes`. See
> **Hard Prerequisites** below — they are mandatory, not optional.

## Defense layers (in order of authority)

| Layer | What it does | Authority |
|-------|--------------|-----------|
| 1. **Bucket privatization** (HARD prerequisite) | Disable r2.dev, remove unprotected custom domains | Cloudflare control plane — only you can do this |
| 2. **`verify-policy.mjs`** (authoritative verification) | Queries Cloudflare API for actual public entry points, probes each | Cloudflare REST API — not human-declared |
| 3. **`BucketPrivacyAttested`** (HARD prerequisite at app save) | Admin explicitly attests bucket is private | Legal/operational commitment, enforced by backend |
| 4. **Worker `r2-access-policy.js`** (CDN boundary) | Default-deny, allows only `agnes-chat/` + `image-generation/` | Only protects routes in `wrangler.toml` |
| 5. **Backend `verifyBackupPrefixNotPublic`** (defense-in-depth) | Probes `PublicBaseURL + AdditionalPublicBaseURLs` at save/test time | Application-level, can be bypassed by undeclared domains |
| 6. **Unguessable backup paths** (last resort) | Random UUIDs in backup keys | Slows guessing, NOT access control |

**Layers 1–3 are HARD prerequisites.** Layers 4–6 are defense-in-depth and cannot
substitute for bucket privatization. Layer 5 in particular relies on the admin
declaring all public entry points via `AdditionalPublicBaseURLs` — if the admin
forgets one, the backend cannot detect it. Use `verify-policy.mjs` (Layer 2) to
close that gap with authoritative Cloudflare API data.

## ⚠️ Hard Prerequisites (must complete before enabling PublicBaseURL)

The Worker only protects the custom domain routed to it (`routes` in
`wrangler.toml`). R2 buckets can be exposed through **multiple** public entry
points that the Worker does NOT cover. You must ensure the Worker-protected
domain is the **only** public way to read the bucket:

1. **Disable R2 Public Development URL** (the `*.r2.dev` endpoint)
   - Cloudflare Dashboard → R2 → `<bucket>` → Settings → Public Development URL
   - **Turn OFF** "Allow Access". When enabled, the bucket is reachable at
     `https://pub-<hash>.r2.dev` and the Worker does NOT sit in front of it —
     `backups/` would be publicly readable there.
   - This is the single most common bypass. Verify it is OFF.

2. **Remove or Worker-protect all other custom domains**
   - Cloudflare Dashboard → R2 → `<bucket>` → Settings → Custom Domains
   - Every custom domain listed there is a public entry point. For each:
     - **Preferred:** remove it (so only the Worker-protected domain remains), or
     - **Alternative:** route it through the same Worker (add it to `routes`)
       so the same allow/deny policy applies.
   - A custom domain that is NOT routed through the Worker bypasses all
     protections.

3. **Do not use a bucket-level public-read policy**
   - R2 buckets should be private by default. Public access must come ONLY
     through the Worker (which allows `agnes-chat/`, `image-generation/` and
     denies everything else).
   - Check: Cloudflare Dashboard → R2 → `<bucket>` → Settings — there is no
     "public read" toggle at the bucket level in R2, but verify no
     misconfiguration grants anonymous read.

4. **Prefer keeping the bucket private + presigned URLs**
   - If you do not need permanent public direct links for Agnes images, the
     simplest secure configuration is: leave `PublicBaseURL` empty in the app,
     keep the bucket fully private, and use presigned URLs (default behavior).
     In this case you do **not** need this Worker at all.

## Authoritative verification with `verify-policy.mjs`

The application backend's probe (`verifyBackupPrefixNotPublic`) only checks the
URLs the admin declared (`PublicBaseURL + AdditionalPublicBaseURLs`). If the
admin forgets to list a domain, the backend cannot detect it. **This is a known
limitation of declaration-based defenses.**

`verify-policy.mjs` closes this gap by querying the **Cloudflare REST API** for
the actual list of public entry points (r2.dev status + all custom domains), then
probing each one. Its input is authoritative, not human-declared.

### Prerequisites

Create a Cloudflare API Token with **Workers R2 Storage Read** permission:
- Dashboard → My Profile → API Tokens → Create Token → Custom token
- Permissions: Account → Workers R2 Storage → Read
- (Optional) TTL and IP restrictions for production

### Usage

```bash
# Required
export CF_ACCOUNT_ID="your-account-id"
export CF_API_TOKEN="your-api-token"
export R2_BUCKET_NAME="your-shared-bucket-name"

# Optional
export CF_JURISDICTION="default"   # "default" | "eu" | "fedramp"
export EXTRA_PUBLIC_URLS="https://cdn2.example.com,https://third-party-cdn.com"

node verify-policy.mjs
```

### What it checks

1. **Step 1**: `GET /accounts/{id}/r2/buckets/{bucket}/domains/managed`
   - If r2.dev is **enabled** → FAIL immediately (must be disabled)
   - If disabled or not configured → OK
2. **Step 2**: `GET /accounts/{id}/r2/buckets/{bucket}/domains/custom`
   - Lists ALL custom domains bound to the bucket (authoritative)
3. **Step 3**: For each `enabled=true` custom domain with active ownership/SSL,
   probe `https://{domain}/backups/.policy-probe-<uuid>` (GET + Range)
   - Expect 403/401 (access denied = Worker policy effective)
   - 404/200/206/3xx/5xx = FAIL (backups exposed or unverifiable)
4. **Step 4** (optional): Probe URLs from `EXTRA_PUBLIC_URLS`
   - Use this for non-Cloudflare entry points (third-party CDNs, mirror domains)

### Exit codes

- `0` — All public entry points correctly deny `backups/`
- `1` — One or more checks failed (r2.dev enabled, custom domain exposes backups, etc.)
- `2` — Missing environment variables

### Recommended cadence

- **Post-deploy**: Run once after deploying the Worker and configuring the bucket.
- **Periodic (CI/cron)**: Run daily to detect configuration drift (e.g., someone
  re-enables r2.dev or adds a custom domain without Worker protection).
- **Pre-save (manual)**: Run before saving `PublicBaseURL` in the app admin UI.

## Application-side enforcement (`BucketPrivacyAttested`)

When the admin sets `PublicBaseURL` in the app admin UI, the backend requires
`BucketPrivacyAttested = true` to save or test the config. This is the admin's
explicit legal/operational commitment that:

- R2 Public Development URL is disabled
- All custom domains are removed or routed through the Worker
- The bucket has no public-read policy
- `AdditionalPublicBaseURLs` is complete (if any remain)

The backend cannot directly read Cloudflare control-plane configuration, so this
attestation is the application-level HARD gate. **It does not replace
`verify-policy.mjs`** — the script provides authoritative verification using
Cloudflare API data, while attestation is a human commitment.

## Verification in the app admin UI

After completing the prerequisites above:

- Save the S3 config with `Public Base URL` = your Worker-protected domain and
  `Bucket Privacy Attested` checked. The backend probes
  `backups/.policy-probe-<uuid>` on save and refuses to save if:
  - Attestation is not checked (`BUCKET_PRIVACY_NOT_ATTESTED`)
  - The probe returns 404/200/206/3xx/5xx or is unreachable
    (`BACKUP_PREFIX_PUBLICLY_READABLE`, fail-closed)
- Run "Test Connection" in the admin UI — it enforces the same checks.
- If you listed additional public entry points in `additional_public_base_urls`,
  each is probed individually; any that return 404/200/3xx/5xx or are unreachable
  will block the save.

## Files

- `r2-access-policy.js` — the Worker script (default-deny, allows only
  `agnes-chat/` and `image-generation/`).
- `wrangler.toml.example` — deployment config template.
- `verify-policy.mjs` — authoritative verification script (queries Cloudflare
  API for r2.dev status + custom domains, probes each).

## Policy Summary

| Prefix | Anonymous GET | Anonymous HEAD | Other methods |
|--------|---------------|----------------|---------------|
| `agnes-chat/` | ✅ allowed | ✅ allowed | ❌ 405 |
| `image-generation/` | ✅ allowed | ✅ allowed | ❌ 405 |
| `backups/` | ❌ 403 | ❌ 403 | ❌ 405 |
| (anything else) | ❌ 403 | ❌ 403 | ❌ 405 |
