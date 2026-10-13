package wire

// TenantPolicy is GET /v1/my/tenant-policy: the organization's name and the
// org-admin policy a member's surfaces follow. The owner's bridge reads
// RequireSandboxForBorrow (borrowed drives must run sandboxed); the web's
// share dialog offers the link access modes in ShareAccess, and publishing
// to the organization says whether an admin approves it.
type TenantPolicy struct {
	TenantID                string `json:"tenantId"`
	Name                    string `json:"name"`
	RequireSandboxForBorrow bool   `json:"requireSandboxForBorrow"`
	// ShareAccess is the link access modes the organization allows, in
	// ShareAccessModes order: store.Tenant.ShareAccessAllowed's answer for
	// each, so no client re-decides it.
	ShareAccess     []string `json:"shareAccess" tstype:"ShareAccess"`
	PublishApproval string   `json:"publishApproval" tstype:"PublishApproval"`
}
