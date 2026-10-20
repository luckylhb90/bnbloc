package contentshare

import (
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// NeedsApproval is the ONE answer to "does a member's publish to this
// organization wait for an org admin" (docs/prd/2026-10-13-app-gallery.md
// §11), for every content kind and every publish path (the publish route,
// a member's click on a publish_component card included, a new version's
// resubmission): only
// an organization whose admins set publishApproval to "none" publishes at
// once. Anything else, the default "required" and an unset or unknown
// value alike, waits for approval (fail closed). Platform scope is
// unaffected: it stays a signed install by a super admin.
func NeedsApproval(t store.Tenant) bool {
	return t.PublishApproval != wire.PublishApprovalNone
}
