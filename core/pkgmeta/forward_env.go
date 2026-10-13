package pkgmeta

import "strings"

// reservedForwardEnv are the names a member may never forward into a
// sandboxed run: bnbloc's own variables (BNBLOC_*, the space-scoped exec
// token among them), the guest user's identity and paths (the space sets
// them), and the dynamic loader's. An entry ending in "*" is a prefix.
var reservedForwardEnv = []string{
	"BNBLOC_*",
	"HOME", "USER", "LOGNAME", "SHELL", "PATH", "PWD", "OLDPWD", "TMPDIR", "HOSTNAME",
	"LD_*", "DYLD_*",
}

// ValidForwardEnv is the one rule for an environment variable pattern a
// member lets into the sandboxed runs of their own agents (Settings → Agent
// sandbox → "Environment passed to sandboxed agents", the bridge's
// BNBLOC_SANDBOX_ENV, the execution descriptor's env): an env name
// (ValidEnvName), or one followed by "*" for every name with that prefix.
// A pattern that can match a reserved name is refused (reservedForwardEnv).
// The desktop holds a copy (valid_forward_env), pinned to this one by
// testdata/forward_env_patterns.json.
func ValidForwardEnv(pattern string) bool {
	name, isPrefix := strings.CutSuffix(pattern, "*")
	if !ValidEnvName(name) {
		return false
	}
	for _, r := range reservedForwardEnv {
		owned, ownedPrefix := strings.CutSuffix(r, "*")
		switch {
		case isPrefix && strings.HasPrefix(owned, name):
			return false // the pattern covers a reserved name or prefix
		case ownedPrefix && strings.HasPrefix(name, owned):
			return false // the name or prefix lies under a reserved prefix
		case name == owned:
			return false
		}
	}
	return true
}
