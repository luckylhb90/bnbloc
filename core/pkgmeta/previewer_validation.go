package pkgmeta

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
)

// KindPreviewerPackageV1 is the package-envelope kind for a previewer: a
// static web bundle the web client runs in a sandboxed opaque-origin iframe
// to render artifact files of the MIME types / extensions it declares
// (docs/architecture/previewer-components.md). Nothing is launched: the
// bundle is served from its sha256-pinned artifact (gateway route in the
// browser, the desktop's own scheme in the shell) and never gets network
// access.
const KindPreviewerPackageV1 = "bnbloc.previewer.package/v1"

// PreviewerContractV1 pins the host <-> frame wire contract (the frame
// protocol in web/previewer-sdk/src/protocol.ts).
const PreviewerContractV1 = "bnbloc.previewer.contract/v1"

// RuntimeBundle is a static bundle: no process, no endpoint.
const RuntimeBundle Runtime = "bundle"

// PreviewerBundleArtifact is the one artifact key of a previewer package.
const PreviewerBundleArtifact = "bundle"

// The code-owned previewer vocabularies. They mirror the frame protocol in
// web/previewer-sdk/src/protocol.ts (PREVIEWER_CAPABILITIES, TOOLBAR_KINDS,
// TOOLBAR_ICONS); TestPreviewerVocabulariesMatchFrameProtocol keeps them equal.
var (
	PreviewerCapabilities = []string{"thumbnail", "liveState", "toolbar", "selection"}
	PreviewerToolbarKinds = []string{"button", "toggle", "zoom"}
	PreviewerToolbarIcons = []string{
		"zoom-in", "zoom-out", "fit", "expand", "grid", "layers", "background", "frames",
		"play", "pause", "reload", "eye", "ruler", "crop", "palette", "info",
	}
)

// Previewer bounds (PRD "Previewer contract details").
const (
	maxPreviewerMatches      = 32
	maxPreviewerToolbar      = 20
	maxPreviewerLabel        = 40
	minPreviewerRank         = 1
	maxPreviewerRank         = 1000
	previewerCapabilityTool  = "toolbar"
	previewerCapabilityThumb = "thumbnail"
	// Suggestions (the Ask AI panel's starting prompts) mirror the frame
	// protocol's SUGGESTION_* limits.
	maxPreviewerSuggestions      = 6
	maxPreviewerSuggestionLabel  = 40
	maxPreviewerSuggestionPrompt = 500
)

var (
	// type/subtype or type/* (RFC 6838 restricted-name characters, lower case).
	previewerMimeRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,62}/(\*|[a-z0-9][a-z0-9!#$&^_.+-]{0,126})$`)
	previewerExtensionRE = regexp.MustCompile(`^(\.[a-z0-9][a-z0-9_-]{0,15}){1,3}$`)
	previewerToolbarIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

func validatePreviewerPackage(pkg Package) error {
	if pkg.Contract != PreviewerContractV1 {
		return fmt.Errorf("unknown previewer contract %q", pkg.Contract)
	}
	if pkg.Runtime != RuntimeBundle {
		return fmt.Errorf(`previewer runtime must be "bundle"`)
	}
	if len(pkg.Permissions.Network) > 0 {
		return fmt.Errorf("previewer bundles never get network access; remove permissions.network")
	}
	if len(pkg.Artifacts) != 1 {
		return fmt.Errorf("previewer requires exactly one artifact %q", PreviewerBundleArtifact)
	}
	if _, ok := pkg.Artifacts[PreviewerBundleArtifact]; !ok {
		return fmt.Errorf("previewer requires exactly one artifact %q", PreviewerBundleArtifact)
	}
	if pkg.Previewer == nil {
		return fmt.Errorf("previewer package requires previewer")
	}
	return validatePreviewer(*pkg.Previewer)
}

func validatePreviewer(p Previewer) error {
	if !core.ValidBundlePath(p.Entry) || !strings.HasSuffix(p.Entry, ".html") {
		return fmt.Errorf("previewer.entry must be a relative .html path inside the bundle")
	}
	if err := validatePreviewerMatch(p.Match); err != nil {
		return err
	}
	if p.Rank < minPreviewerRank || p.Rank > maxPreviewerRank {
		return fmt.Errorf("previewer.rank must be %d-%d", minPreviewerRank, maxPreviewerRank)
	}
	caps := map[string]bool{}
	for _, c := range p.Capabilities {
		if !contains(PreviewerCapabilities, c) {
			return fmt.Errorf("unknown previewer capability %q", c)
		}
		if caps[c] {
			return fmt.Errorf("duplicate previewer capability %q", c)
		}
		caps[c] = true
	}
	if len(p.Toolbar) > 0 && !caps[previewerCapabilityTool] {
		return fmt.Errorf("previewer.toolbar requires the %q capability", previewerCapabilityTool)
	}
	if err := validatePreviewerSuggestions(p.Suggestions); err != nil {
		return err
	}
	return validatePreviewerToolbar(p.Toolbar)
}

// validatePreviewerSuggestions checks the Ask AI panel's starting prompts:
// at most maxPreviewerSuggestions, each a short label and a prompt.
func validatePreviewerSuggestions(items []PreviewerSuggestion) error {
	if len(items) > maxPreviewerSuggestions {
		return fmt.Errorf("previewer.suggestions holds at most %d items", maxPreviewerSuggestions)
	}
	for i, it := range items {
		if n := utf8.RuneCountInString(it.Label); strings.TrimSpace(it.Label) == "" || n > maxPreviewerSuggestionLabel {
			return fmt.Errorf("previewer.suggestions[%d].label must be 1-%d characters", i, maxPreviewerSuggestionLabel)
		}
		if n := utf8.RuneCountInString(it.Prompt); strings.TrimSpace(it.Prompt) == "" || n > maxPreviewerSuggestionPrompt {
			return fmt.Errorf("previewer.suggestions[%d].prompt must be 1-%d characters", i, maxPreviewerSuggestionPrompt)
		}
	}
	return nil
}

func validatePreviewerMatch(m PreviewerMatch) error {
	if len(m.MimeTypes)+len(m.Extensions) == 0 {
		return fmt.Errorf("previewer.match requires at least one MIME type or extension")
	}
	if len(m.MimeTypes) > maxPreviewerMatches || len(m.Extensions) > maxPreviewerMatches {
		return fmt.Errorf("previewer.match lists hold at most %d entries", maxPreviewerMatches)
	}
	seen := map[string]bool{}
	for _, mt := range m.MimeTypes {
		if !previewerMimeRE.MatchString(mt) {
			return fmt.Errorf("previewer.match.mimeTypes: %q must be a lower-case type/subtype or type/*", mt)
		}
		if seen[mt] {
			return fmt.Errorf("previewer.match.mimeTypes: duplicate %q", mt)
		}
		seen[mt] = true
	}
	for _, ext := range m.Extensions {
		if !previewerExtensionRE.MatchString(ext) {
			return fmt.Errorf("previewer.match.extensions: %q must be a lower-case .ext (up to three parts)", ext)
		}
		if seen[ext] {
			return fmt.Errorf("previewer.match.extensions: duplicate %q", ext)
		}
		seen[ext] = true
	}
	return nil
}

func validatePreviewerToolbar(items []PreviewerToolbarItem) error {
	if len(items) > maxPreviewerToolbar {
		return fmt.Errorf("previewer.toolbar holds at most %d items", maxPreviewerToolbar)
	}
	ids := map[string]bool{}
	for i, it := range items {
		if !previewerToolbarIDRE.MatchString(it.ID) {
			return fmt.Errorf("previewer.toolbar[%d].id %q must match [a-z][a-z0-9-]{0,31}", i, it.ID)
		}
		if ids[it.ID] {
			return fmt.Errorf("previewer.toolbar: duplicate id %q", it.ID)
		}
		ids[it.ID] = true
		if !contains(PreviewerToolbarIcons, it.Icon) {
			return fmt.Errorf("previewer.toolbar[%d].icon %q is not a known icon", i, it.Icon)
		}
		if !contains(PreviewerToolbarKinds, it.Kind) {
			return fmt.Errorf("previewer.toolbar[%d].kind %q must be button, toggle, or zoom", i, it.Kind)
		}
		if n := utf8.RuneCountInString(it.Label); strings.TrimSpace(it.Label) == "" || n > maxPreviewerLabel {
			return fmt.Errorf("previewer.toolbar[%d].label must be 1-%d characters", i, maxPreviewerLabel)
		}
	}
	return nil
}

// PreviewerDeclaresThumbnail reports whether a previewer may provide
// thumbnails (the host stores them only then).
func PreviewerDeclaresThumbnail(p Previewer) bool {
	return contains(p.Capabilities, previewerCapabilityThumb)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func init() { RegisterKindValidator(KindPreviewerPackageV1, validatePreviewerPackage) }
