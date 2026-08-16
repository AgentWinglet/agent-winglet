# Winglet App + Hook Auth/Billing Gate Spec

Status: draft
Date: 2026-08-16
Related repo: `~/workplace/agent-winglet-site`

Site-side scope landed on the site repo's `add-app-verification` branch
(not yet merged to `main`): app entitlement issue/refresh endpoints and
signing. This spec has been updated to match that implementation rather
than the original device-code design. See `Site API Contract` and
`Known Gaps` below.

## Purpose

Add Firebase auth gating and Paddle subscription gating to the Wails desktop app
and the Claude/Codex hooks without weakening Winglet's core privacy claim:
project files, prompts, transcripts, tool output, paths, and savings stats stay
on the user's machine.

The website is the billing/auth control plane. This repo is the local product.
The local product should only need a server-issued, signed entitlement file to
decide whether paid behavior is enabled.

For local testing against the companion site branch, the app must be able to use
`http://localhost:3000` as the site base URL for login, pricing, entitlement
issue, and entitlement refresh.

Use `AGENT_WINGLET_SITE_BASE_URL=http://localhost:3000` as the local testing
override. When unset, default to `https://agentwinglet.com`.

## Current Shape

- Desktop app: Go/Wails backend in `cmd/agent-winglet-app`, plain Vite frontend
  in `cmd/agent-winglet-app/frontend`.
- Hooks: `cmd/claude-hook` and `cmd/codex-hook`.
- Global local config: `~/.agent-winglet/config.json`.
- Project state/stats: `~/.agent-winglet/projects/<project>-<hash>/`.
- There is no auth, billing, account identity, or networked service today.
- Site side (agent-winglet-site): Firebase client auth, Firebase Admin
  ID-token verification, and Paddle checkout/customer portal/webhook
  subscription sync already exist and are unrelated to this work. The
  `add-app-verification` branch adds the app-facing entitlement
  issue/refresh endpoints and JWS signing this spec depends on.

## Product Decision

Auth and subscription checks must gate:

- App dashboard access, settings, install toggles, and future account screens.
- Hook savings behavior.

Hook installation and hook authorization are separate. Users may install or
enable the Claude/Codex hook entries before they are logged in or subscribed,
but installed hooks must not perform any Winglet behavior until the app has
successfully checked with the site and written a valid entitlement for this
device. "Hook configured" only means the agent can invoke the binary; it is not
proof that the hook is allowed to work.

Auth and subscription checks must not gate:

- Clean uninstall.
- Viewing a clear "sign in / subscribe / expired" status.
- Hook pass-through behavior. If unauthorized, hooks should no-op rather than
  breaking Claude Code or Codex.
- Direct account actions: sign in, open pricing, manage billing when the signed
  in account has a Paddle customer, sync/refresh, and sign out.

Active entitlement states:

- `trialing`: allowed.
- `active`: allowed.
- `past_due`, `canceled`, `paused`, `expired`, missing, invalid signature:
  denied. No grace period for `past_due` — it is denied the same as
  `canceled`, not given extra time.

The site's current `createEntitlementClaims` only computes `active` or
`canceled` from `subscription.subscriptionActive`; it does not yet map
Paddle's `trialing`/`past_due`/`paused` lifecycle states onto the
entitlement. Hooks and the app must still implement the full state
handling below so nothing changes on the local side once the site fills
this in — see `Known Gaps`.

## Architecture

Use three layers.

1. Site control plane

The Next site owns Firebase auth, Firestore user records, Paddle checkout,
Paddle customer portal links, webhooks, and entitlement issuance.

2. Local app

The Wails app owns sign-in UX (in-webview Firebase auth, not a browser
handoff), account status display, subscription refresh, and local
entitlement persistence. It is the only local component that calls the site.
Before hooks can work, the app must call the site entitlement API and persist
the server response locally.

3. Hooks

The hooks never authenticate with Firebase or Paddle. They read a local signed
entitlement, verify it offline, and decide whether to run paid transformations.
That offline hot path is allowed only because the entitlement was created by a
prior server check. If no server-issued entitlement exists, or if it is expired,
invalid, missing `hook_savings`, or carries an inactive subscription status, the
hooks must treat Winglet as unavailable.

This split keeps hook startup fast and avoids network calls inside agent
tool-use paths.

## Local Files

Add a dedicated local auth package, separate from existing stats/config files:

```text
~/.agent-winglet/
  config.json
  auth.json
  entitlement.jws
  projects/
```

`auth.json` should contain only account/session metadata:

```json
{
  "siteBaseURL": "https://agentwinglet.com",
  "uidHash": "sha256:...",
  "emailHint": "u***@example.com",
  "deviceId": "dev_...",
  "refreshToken": "opaque_site_refresh_token",
  "lastRefreshAt": "2026-08-14T12:00:00Z"
}
```

`siteBaseURL` defaults to `https://agentwinglet.com` for production builds. For
local app/site integration testing on the `add-app-verification` branch, set
`AGENT_WINGLET_SITE_BASE_URL=http://localhost:3000`; persist the resolved value
in `auth.json` as `siteBaseURL`. All app-created URLs and API calls are
resolved against this value:

- login and in-app Firebase auth: `siteBaseURL`
- pricing/checkout entry point: `${siteBaseURL}/#pricing`
- entitlement issue: `${siteBaseURL}/api/app/entitlements/issue`
- entitlement refresh: `${siteBaseURL}/api/app/entitlements/refresh`

`entitlement.jws` is a compact JWS signed by the site with a private key. This
repo embeds only the public verification key.

Entitlement claims:

```json
{
  "iss": "https://agentwinglet.com",
  "aud": "agent-winglet-local",
  "sub": "sha256:<firebase_uid>",
  "device_id": "dev_...",
  "plan": "winglet_pro",
  "status": "active",
  "features": ["hook_savings", "desktop_dashboard"],
  "issued_at": 1786708800,
  "expires_at": 1787313600,
  "grace_until": 1787313600,
  "entitlement_version": 1
}
```

`grace_until` currently equals `expires_at` and, per the product decision
above, there is no grace period for `past_due` to apply it to — status
gating (`Hook Work`) must deny `past_due` outright rather than reading
`grace_until`. Local verification should still parse the field rather than
error on it, in case the site starts using it for something else, but
nothing in this spec depends on its value today. Don't confuse this with
the unrelated "offline grace" in `Refresh behavior`, which is about
tolerating a failed network refresh against an entitlement already valid
until `expires_at`.

The site signs with whichever key type is configured, not a fixed
algorithm: `RS256` (RSA-SHA256) if the configured private key is RSA, or
`EdDSA` (Ed25519) if it's Ed25519. The compact JWS header includes `alg`,
`typ: "JWT"`, and a `kid` matching `ENTITLEMENT_SIGNING_KEY_ID`. The Go
verifier in this repo must:

- Read `alg`/`kid` from the header and select the matching embedded public
  key rather than assuming one fixed algorithm.
- Support both `RS256` and `Ed25519` verification paths, since the site's
  configured key type isn't pinned in this spec and could change per
  environment.
- Reject any other `alg` value.

Do not use shared secrets in the app or hooks.

Expiry policy (from the site's `lib/entitlements.ts`):

- `active` / `trialing`: 7 days.
- `past_due`: 3 days.
- `canceled` / anything inactive: 1 hour.
- Refresh is denied outright if the app session (`appSessions/{sessionId}`)
  has been revoked.

File permissions:

- Create `~/.agent-winglet` as `0700`.
- Write auth/token files as `0600`.
- Use atomic writes and a small file lock around refreshes.
- Never store Firebase service account keys, Paddle API keys, or webhook secrets
  locally.

## Site API Contract

There is no browser device-code/polling flow. The Wails frontend signs in
directly inside the app's own webview using Firebase client auth, then
exchanges the resulting Firebase ID token with the site. This is
significantly simpler than the originally planned flow and is what's
actually implemented on `add-app-verification`.

Base URL:

- Production: `https://agentwinglet.com`
- Local testing: `AGENT_WINGLET_SITE_BASE_URL=http://localhost:3000`

The desktop app must not hardcode production-only URLs. The same base URL must
drive Firebase auth pages, pricing, and entitlement API calls so the app can be
tested against the local site branch without code changes.

- `POST /api/app/entitlements/issue`
  - Called once, right after in-webview Firebase sign-in.
  - Header: `Authorization: Bearer <firebase_id_token>`.
  - Body: `{ deviceId, deviceName?, platform?, appVersion? }`.
  - The site verifies the ID token with Firebase Admin, syncs the existing
    `users/{uid}` doc, creates an `appSessions/{sessionId}` record, and
    returns:
    `{ refreshToken, entitlement, account: { accountId, email, subscription: { product, tier, status, active } } }`.
  - `refreshToken` is opaque (`war_<sessionId>.<secret>`); the site stores
    only its SHA-256 hash.

- `POST /api/app/entitlements/refresh`
  - Body: `{ deviceId, refreshToken }`.
  - Validates the refresh token hash, device id match, and that the session
    isn't revoked, then returns a fresh `{ entitlement, account }` (same
    shapes as `issue`).
  - No Authorization header / Firebase ID token needed — the opaque refresh
    token is the credential.

There is currently **no revoke endpoint**, even though `refreshAppEntitlement`
already checks `session.revokedAt` and will honor a revoked session once one
exists. `Logout()` in the app has nothing server-side to call yet — see
`Known Gaps`.

Pricing/subscription entry:

- The app's pricing action opens `${siteBaseURL}/#pricing`.
- Checkout is owned by the site and Paddle. The app does not call Paddle
  directly and does not store Paddle credentials.
- After checkout, the app refreshes entitlement status through
  `/api/app/entitlements/refresh` and keeps hooks disabled until the refreshed
  entitlement includes `hook_savings`.

Login entry:

- The primary login path is inside the Wails app using the Firebase client SDK.
- If the app also offers an "Open Winglet account" link, it must use
  `siteBaseURL`, not a production-only URL.

The app must not upload paths, prompts, transcripts, command output, savings
events, session ids, or project ids. Acceptable request fields are product
version, OS/arch, device id, auth token, and coarse entitlement metadata.

## Desktop App Work

Sign-in happens inside the app's own webview, not a browser handoff: the
Vite frontend gets the Firebase client SDK and signs in directly (Google,
or email magic link / email+password if Google sign-in is unreliable in
the Wails webview), then the Go backend takes over for the token exchange.
There is no device code, no `loginUrl`, and no polling loop.

Backend:

- Add `internal/authstate` for reading/writing `auth.json`.
- Add `internal/entitlement` for parsing and verifying `entitlement.jws`
  (must handle both `RS256` and `Ed25519`/`EdDSA` per the `kid`/`alg` in
  the JWS header — see `Local Files`).
- Add `internal/siteapi` for the issue/refresh calls.
- Add Wails methods:
  - `GetAccountStatus()`
  - `GetSiteBaseURL()` — returns the resolved site base URL so frontend login
    and pricing links use the same origin as entitlement API calls.
  - `CompleteFirebaseSignIn(idToken string)` — frontend calls this right
    after `user.getIdToken()` succeeds; backend calls
    `POST /api/app/entitlements/issue` and persists `auth.json` +
    `entitlement.jws`.
  - `RefreshEntitlement()`
  - `Logout()` — clears local `auth.json`/`entitlement.jws`; there is no
    server-side session revoke to call yet (see `Known Gaps`).
  - `OpenBillingPortal()`
  - `OpenPricing()` — opens `${siteBaseURL}/#pricing`; with
    `AGENT_WINGLET_SITE_BASE_URL=http://localhost:3000`, opens
    `http://localhost:3000/#pricing`.
- Keep current stats methods local-only. Do not mix stats payloads into auth
  refresh calls.

Frontend:

- Add an account gate before the existing dashboard shell.
- States: signed out, signing in, subscribed, past due (denied, shown the
  same as expired/unsubscribed — no grace state in the UI), expired,
  network unavailable, and server error. (`trialing` exists in the UI
  design but the site can't produce it yet — see `Known Gaps`.)
- Add account area in settings with email hint, plan, next renewal/expiry, sync
  button, manage billing, and sign out.
- Existing overview/projects views require `desktop_dashboard` entitlement.
- Install toggles should be disabled when entitlement is missing, with a clear
  account action.
- Add a login action inside the app. It signs in with Firebase in the app
  webview, calls `CompleteFirebaseSignIn(idToken)`, and shows the returned
  account/subscription state.
- Add a pricing action. It opens `${siteBaseURL}/#pricing` so a signed-in user
  can subscribe through the site/Paddle flow. For local testing this is
  `http://localhost:3000/#pricing`.

Refresh behavior:

- Refresh at app startup.
- Refresh immediately after login and after returning from pricing/checkout.
- Refresh when the account/settings screen opens.
- Refresh at most once per 24 hours automatically otherwise. Entitlements
  now last 7 days (`active`/`trialing`) or 3 days (`past_due`), so the
  original 12-hour cadence was tuned for a shorter-lived token than the
  site actually issues.
- If refresh fails but an existing entitlement's `expires_at` (and
  `grace_until`, once the site sets it independently) hasn't passed, show
  "offline grace" and keep hooks enabled. Don't assume a buffer beyond
  `expires_at` exists today — see the `grace_until` note in `Local Files`.
- If refresh fails and there is no still-valid entitlement, show a server/login
  problem in the app and keep hooks disabled.

## Hook Work

Add one shared gate package used by both hook binaries:

```text
internal/entitlement/
  entitlement.go
  entitlement_test.go
```

Hook behavior:

- Load and verify `~/.agent-winglet/entitlement.jws` before any existing hook
  work runs. This check happens on every invocation and must run before
  registering projects, mutating ledger/phase/retire state, recording stats,
  deduping, budgeting, retiring output, compact nudges, or savings receipts.
- Require `hook_savings` feature.
- Permit only `active` or `trialing`. Deny `past_due` immediately — no
  grace period.
- On denied state, perform no savings transformations and do not write savings
  stats. The underlying Claude Code/Codex action continues normally.
- Emit at most one short auth/subscription notice per agent session per agent.
- Do not call the network.
- Do not block the underlying agent command.

Agent-facing denied messages must be direct and consistent in both hooks:

- Missing `auth.json`, missing `entitlement.jws`, invalid local session, or no
  prior successful server check:
  `Winglet is installed but is not signed in. Open Winglet and sign in to enable hook savings.`
- Valid account but `canceled`, `paused`, `past_due`, expired, missing
  `hook_savings`, or otherwise unsubscribed:
  `Winglet is installed but your subscription is not active. Open Winglet pricing to subscribe and enable hook savings.`
- Network/server refresh failures are not generated by hooks because hooks do
  not call the network. The app surfaces those errors; hooks only report the
  resulting local state.

Use each agent's native hook response shape for the notice (`systemMessage` or
`additionalContext` as appropriate), but keep the text recognizable and visible
inside both Claude Code and Codex sessions.

Tests:

- Valid signed entitlement enables hook behavior.
- Expired entitlement disables hook behavior.
- Bad signature disables hook behavior.
- Missing entitlement disables hook behavior.
- Past-due always disables hook behavior, regardless of `expires_at` or
  `grace_until` — no grace period.
- Hook output remains valid JSON / valid hook response in every denied state.
- No hook package imports `net/http`, `net`, or site API packages.
- Cover `trialing`/`past_due`/`paused` even though the site only emits
  `active`/`canceled` today (see `Known Gaps`) — the verifier shouldn't need
  a hook-side change once the site fills those in.
- Cover both `RS256` and `Ed25519` signatures, and reject an unrecognized
  `alg`.

## Installer / Distribution Work

Open-source install from source creates a clone-resistance problem: anyone with
the source can remove local gate checks. Treat this as a business/legal problem,
not something the local code can fully solve.

Preferred distribution model:

- Public repo contains transparent UI, hook protocol adapters, tests, docs, and
  privacy guarantees.
- Paid releases are official signed binaries built by Winglet CI.
- The site only serves signed release artifacts to entitled users.
- The entitlement signing private key never enters the repo or local build.
- The public key embedded in this repo can verify official entitlements but
  cannot mint them.

If the full hook logic is open source under MIT, clone resistance is weak. A
coding agent can remove the checks. At most, the official service, official
updates, signing keys, trademark, Paddle/Firebase backend, and distribution
channel remain protected. Don't promise that open source code can't be
cloned — say precisely what stays protected instead (matches the site
spec's framing on `add-app-verification`).

Stronger options:

- Use a source-available noncommercial license instead of MIT for the product
  repo.
- Keep a small paid optimization engine closed-source and call it from open
  hook adapters.
- Keep only docs/site open source and distribute the app/hooks as signed
  binaries.
- Open-source the privacy-critical readers and tests, but keep entitlement and
  paid transformation logic closed.

## Privacy / Security Proof Plan

If the product remains closed source, trust needs artifacts:

- Publish a local-only threat model that lists every network request and proves
  none contain project content.
- Add CI tests that fail if hook packages import networking packages.
- Add integration tests with a local fake site proving app auth refresh sends
  only account/device fields.
- Publish SBOMs and signed checksums for every release.
- Code-sign and notarize releases where platform tooling supports it.
- Publish a reproducible-build recipe or at least deterministic build logs.
- Commission a third-party audit focused on:
  - no prompt/transcript/path exfiltration,
  - entitlement verification,
  - update/download integrity,
  - local file permissions,
  - Firebase/Paddle webhook handling.
- Publish a network transparency report with sample Little Snitch/mitmproxy
  captures from login, refresh, checkout, and normal hook operation.

The key privacy claim should be precise:

"Winglet sends account, billing, device, and version metadata to
agentwinglet.com for login and subscription checks. It does not send prompts,
transcripts, tool output, command output, project paths, files, or savings
stats. Hooks do not make network requests."

## Known Gaps

Things the `add-app-verification` implementation leaves open, tracked here
so app/hook work doesn't silently assume they're done:

- **No revoke endpoint.** `refreshAppEntitlement` already checks
  `session.revokedAt`, but nothing sets it — `Logout()` can only delete
  local files today. Add `POST /api/app/entitlements/revoke` before
  shipping a "sign out everywhere" or "remote wipe a lost laptop" story.
- **Status mapping is binary.** `createEntitlementClaims` only produces
  `active` or `canceled`; Paddle's `trialing`/`past_due`/`paused` states
  aren't wired to the entitlement yet. Until the site adds that mapping,
  a trialing or past-due user will look `canceled` to the app and hooks.
- **Site currently grants features to `past_due`.** `createEntitlementClaims`
  includes `past_due` among the statuses that get `hook_savings`/
  `desktop_dashboard` and a 3-day expiry — i.e. exactly the grace window
  this spec now says shouldn't exist. Hooks are the enforcement point in
  the meantime (deny `past_due` locally regardless of what the JWS
  claims), but the site should stop granting features for `past_due` once
  prioritized so the JWS itself reflects the real policy.
- **`grace_until` has no remaining use for subscription-state gating.**
  It's always set equal to `expires_at`, and with no grace period for
  `past_due`, there's no case left in this spec where it matters for
  status decisions. The "offline grace" in `Refresh behavior` is unrelated
  — it's tolerance for the app failing to reach the network before an
  already-issued, still-unexpired entitlement expires, not a subscription
  grace period.
- **Signing algorithm isn't pinned.** The site picks `RS256` or `EdDSA`
  based on whatever key is configured in `ENTITLEMENT_SIGNING_PRIVATE_KEY`
  per environment. The Go verifier needs to support both from day one
  rather than hardcoding one.
- **No device management UI or revocation list**, and no gated/signed
  binary downloads yet — both are explicit non-goals on the site spec for
  this pass, matching Phase 4 below.

## Implementation Phases

1. Site auth/billing base — mostly landed on `add-app-verification`
   (not yet merged to `main`)
   - Firebase client/admin config, user doc schema, Paddle client/server,
     checkout, webhooks, and customer portal already existed before this
     branch.
   - This branch adds `POST /api/app/entitlements/issue`,
     `POST /api/app/entitlements/refresh`, the `appSessions` collection,
     and JWS entitlement signing (`lib/entitlements.ts`,
     `lib/app-entitlements.ts`).
   - Still open on the site side: the revoke endpoint and the
     `trialing`/`past_due`/`paused` status mapping — see `Known Gaps`.

2. App auth shell
   - Add the account gate, in-webview Firebase sign-in (no device-code
     flow), entitlement storage, and account settings, per the updated
     `Desktop App Work` section above.

3. Hook offline gate
   - Add entitlement verifier and wire it into both hooks as an early
     decision. Support both `RS256` and `Ed25519` signatures from the
     start.

4. Paid distribution
   - Replace public download placeholders with signed, entitlement-gated
     release downloads.

5. Proof package
   - Add tests, docs, release signing, and privacy/security audit artifacts.

## References

- Paddle webhook signature verification:
  `https://developer.paddle.com/webhooks/about/signature-verification/`
- Paddle checkout custom data:
  `https://developer.paddle.com/build/transactions/custom-data/`
- Paddle subscription lifecycle events:
  `https://developer.paddle.com/webhooks/subscriptions/subscription-canceled/`
- Firebase custom tokens:
  `https://firebase.google.com/docs/auth/admin/create-custom-tokens`
- Firebase session cookies:
  `https://firebase.google.com/docs/auth/admin/manage-cookies`
