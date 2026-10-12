package pkgmeta

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MCP tool package assembly (docs/prd/2026-10-12-experts-and-teams.md §6 and
// "Gap closure" 10): the one place the rules of turning an MCP tool draft
// into a signable package live. `bnbloc cores publish-mcp` and the platform
// console's form (POST /v1/platform/mcp-packages) both go through it:
//
//	PrepareMCP(draft, platforms)   before any binary is uploaded
//	AssembleMCP(prepared, pins)    once each platform's binary is staged

// Assembly refusals a caller may word for its own input (CLI flags, form
// fields); the rule itself stays here.
var (
	// ErrMCPNeedsBinaries: a stdio tool names no platform binary.
	ErrMCPNeedsBinaries = errors.New("a stdio MCP tool needs at least one platform binary")
	// ErrMCPEndpointBinaries: an endpoint tool names platform binaries.
	ErrMCPEndpointBinaries = errors.New("only a stdio MCP tool takes platform binaries")
)

// PrepareMCP normalizes and checks an MCP tool draft before its binaries
// exist: the kind and contract default to the MCP ones (another kind is
// refused), a stdio tool names at least one platform (each an upload
// placeholder in Artifacts), an endpoint tool none, and the draft must be
// valid apart from the binary pins. The draft's own Artifacts are replaced
// by the platforms given.
func PrepareMCP(draft Package, platforms []string) (Package, error) {
	if draft.Kind == "" {
		draft.Kind = KindMCPPackageV1
	}
	if draft.Contract == "" {
		draft.Contract = MCPContractV1
	}
	if draft.Kind != KindMCPPackageV1 {
		return Package{}, fmt.Errorf("package kind %q is not %s", draft.Kind, KindMCPPackageV1)
	}
	draft.Artifacts = nil
	switch draft.Runtime {
	case RuntimeStdio:
		if len(platforms) == 0 {
			return Package{}, ErrMCPNeedsBinaries
		}
		draft.Artifacts = map[string]Artifact{}
		for _, p := range platforms {
			p = strings.TrimSpace(p)
			if p == "" {
				return Package{}, fmt.Errorf("a platform binary needs its platform (goos-goarch)")
			}
			if _, dup := draft.Artifacts[p]; dup {
				return Package{}, fmt.Errorf("platform %s is named twice", p)
			}
			draft.Artifacts[p] = Artifact{} // an upload placeholder
		}
	default:
		if len(platforms) > 0 {
			return Package{}, fmt.Errorf("%w (runtime %q)", ErrMCPEndpointBinaries, draft.Runtime)
		}
	}
	if err := ValidatePackage(draft); err != nil {
		return Package{}, err
	}
	return draft, nil
}

// AssembleMCP pins each staged binary into a prepared draft and validates the
// complete package: every prepared platform gets exactly its artifact (url +
// sha256), none is left a placeholder and none is added.
func AssembleMCP(prepared Package, pins map[string]Artifact) (Package, error) {
	if len(prepared.Artifacts) != len(pins) {
		return Package{}, fmt.Errorf("the staged binaries (%s) do not match the platforms (%s)", platformList(pins), platformList(prepared.Artifacts))
	}
	out := prepared
	if len(pins) > 0 {
		out.Artifacts = make(map[string]Artifact, len(pins))
		for p, a := range pins {
			if _, ok := prepared.Artifacts[p]; !ok {
				return Package{}, fmt.Errorf("platform %s was not prepared", p)
			}
			out.Artifacts[p] = a
		}
	}
	if err := ValidateCompleteArtifacts(out); err != nil {
		return Package{}, err
	}
	if err := ValidatePackage(out); err != nil {
		return Package{}, err
	}
	return out, nil
}

func platformList(m map[string]Artifact) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}
