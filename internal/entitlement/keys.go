package entitlement

const DevSigningKeyID = "winglet-dev-2026-08-16"

const devSigningPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAj+2VM1uu1yP80LgNJY3TDqqMnxb9Yf7GS+MIkuv8PiE=
-----END PUBLIC KEY-----`

// embeddedPublicKeys contains public verification keys only. These keys cannot
// mint entitlements; they only verify entitlement.jws values signed by the site.
var embeddedPublicKeys = map[string]string{
	DevSigningKeyID: devSigningPublicKeyPEM,
}
