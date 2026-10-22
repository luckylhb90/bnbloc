package wire

// A room's invite code is short enough to read out and type: InviteCodeLen
// characters of InviteCodeAlphabet (31 symbols; no 0, O, 1, I or L, which
// read alike). That is under a million codes, so it is safe only with the
// rest of the policy: a short life, a throttled redemption and codes found
// per tenant (services/room invite policy, docs/prd/2026-10-22-short-invite-
// codes.md). The room service draws codes from this alphabet; the web types
// and normalizes them by it.
const (
	InviteCodeLen      = 4
	InviteCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)
