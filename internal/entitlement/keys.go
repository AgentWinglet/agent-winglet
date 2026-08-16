package entitlement

const DevSigningKeyID = "winglet-dev-2026-08-16"

const devSigningPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAj+2VM1uu1yP80LgNJY3TDqqMnxb9Yf7GS+MIkuv8PiE=
-----END PUBLIC KEY-----`

// ProdSigningKeyID is the kid agentwinglet.com's production deployment
// currently signs entitlement.jws with (ENTITLEMENT_SIGNING_KEY_ID). Same key
// for every user — see lib/entitlements.ts in agent-winglet-site; there is no
// per-user signing key.
const ProdSigningKeyID = "prod-2026-08-16-fadaed88"

const prodSigningPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA8OHfCYPjMIMXWFLO8yDjgSEUKP1+1FK/kUEY5Jx12OE=
-----END PUBLIC KEY-----`

// embeddedPublicKeys contains public verification keys only. These keys cannot
// mint entitlements; they only verify entitlement.jws values signed by the site.
var embeddedPublicKeys = map[string]string{
	DevSigningKeyID:  devSigningPublicKeyPEM,
	ProdSigningKeyID: prodSigningPublicKeyPEM,
}
