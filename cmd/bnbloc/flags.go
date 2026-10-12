package main

import (
	"crypto/ed25519"
	"flag"
)

// gatewayFlags are the --gateway/--token pair of every subcommand that calls
// the gateway (resolveConfig layers them over the environment and the
// stored sign-in).
type gatewayFlags struct {
	gateway, token *string
}

func addGatewayFlags(fs *flag.FlagSet) gatewayFlags {
	return gatewayFlags{
		gateway: fs.String("gateway", "", "gateway base URL (default: BNBLOC_GATEWAY or the stored sign-in's)"),
		token:   fs.String("token", "", "session token (default: BNBLOC_TOKEN, BNBLOC_API_TOKEN or the stored sign-in)"),
	}
}

func (g gatewayFlags) config() config  { return resolveConfig(*g.gateway, *g.token) }
func (g gatewayFlags) client() *client { return newClient(g.config()) }

// signingFlags are the --seckey/--unsigned/--rollout trio of every
// subcommand that signs and publishes a package.
type signingFlags struct {
	seckey, rollout *string
	unsigned        *bool
}

func addSigningFlags(fs *flag.FlagSet, rolloutUsage string) signingFlags {
	return signingFlags{
		seckey:   fs.String("seckey", "", "ed25519 private key file for signing (default "+defaultSeckeyHint+" when it exists)"),
		unsigned: fs.Bool("unsigned", false, "install without a signature (dev; gateway must allow it)"),
		rollout:  fs.String("rollout", "", rolloutUsage),
	}
}

// key is the signing key the flags select (nil with --unsigned).
func (s signingFlags) key() (ed25519.PrivateKey, error) {
	return packageSigningKey(*s.seckey, *s.unsigned)
}
