package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

// The platform console's MCP tool form (docs/prd/2026-10-12-experts-and-
// teams.md, "Gap closure" 10):
//
//	POST /v1/platform/mcp-packages   an MCP tool draft → the package JSON to sign
//
// A super admin fills the form; a stdio tool's binaries were staged first
// through POST /v1/platform/artifacts (their keys go in the draft). The
// gateway assembles the package with pkgmeta.PrepareMCP/AssembleMCP, the
// assembly `bnbloc cores publish-mcp` uses, pins each binary to its gateway
// artifact URL and sha256, checks the staged bytes, and answers the exact
// bytes to sign. Signing stays offline, like content promote: nothing is
// installed here (the signed file goes to POST /v1/platform/components).

// mcpPackageDraft is the form's body: the MCP package fields without kind,
// contract or pins, and for stdio the staged key of each platform's binary.
type mcpPackageDraft struct {
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	Publisher   string                     `json:"publisher"`
	Description string                     `json:"description,omitempty"`
	Runtime     pkgmeta.Runtime            `json:"runtime"`
	Endpoint    *pkgmeta.Endpoint          `json:"endpoint,omitempty"`
	Permissions pkgmeta.Permissions        `json:"permissions"`
	MCP         *pkgmeta.MCP               `json:"mcp"`
	Artifacts   map[string]mcpStagedBinary `json:"artifacts,omitempty"`
}

// mcpStagedBinary names a binary staged through POST /v1/platform/artifacts.
type mcpStagedBinary struct {
	Key string `json:"key"`
}

var stagedArtifactKeyRE = regexp.MustCompile(`^sha256/[0-9a-f]{64}$`)

func (s *Service) handleBuildMCPPackage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSuperAdminClaims(w, r); !ok {
		return
	}
	var draft mcpPackageDraft
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&draft); err != nil {
		http.Error(w, "invalid json body: "+err.Error(), http.StatusBadRequest)
		return
	}
	base := publicBaseURL(r)
	platforms := make([]string, 0, len(draft.Artifacts))
	pins := make(map[string]pkgmeta.Artifact, len(draft.Artifacts))
	for platform, staged := range draft.Artifacts {
		key := strings.TrimSpace(staged.Key)
		if !stagedArtifactKeyRE.MatchString(key) {
			http.Error(w, fmt.Sprintf("artifacts.%s.key must be the key the artifact upload answered (sha256/<hex>)", platform), http.StatusBadRequest)
			return
		}
		platforms = append(platforms, platform)
		pins[platform] = pkgmeta.Artifact{URL: base + "/v1/artifacts/" + key, SHA256: strings.TrimPrefix(key, "sha256/")}
	}
	sort.Strings(platforms)
	prepared, err := pkgmeta.PrepareMCP(pkgmeta.Package{
		Name: strings.TrimSpace(draft.Name), Version: strings.TrimSpace(draft.Version), Publisher: strings.TrimSpace(draft.Publisher),
		Description: strings.TrimSpace(draft.Description), Runtime: draft.Runtime, Endpoint: draft.Endpoint,
		Permissions: draft.Permissions, MCP: draft.MCP,
	}, platforms)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pkg, err := pkgmeta.AssembleMCP(prepared, pins)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(pkg.Artifacts) > 0 {
		if strings.TrimSpace(s.Backends.Realtime) == "" {
			obs.ServerError(w, "artifact store not configured", nil)
			http.Error(w, "artifact store not configured", http.StatusServiceUnavailable)
			return
		}
		// The staged bytes must be there and match their key, as the install
		// will check: a package pinning a missing binary is refused now.
		if err := s.probeUploadedArtifacts(r.Context(), pkg, base); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	body, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		obs.ServerError(w, "encode mcp package failed", err)
		http.Error(w, "encode mcp package failed", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	buf.Write(body)
	buf.WriteByte('\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.package.json"`, pkg.Name, pkg.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
