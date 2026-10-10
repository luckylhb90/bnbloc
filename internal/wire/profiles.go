package wire

// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
// §1), served by the room service.

// UserProfile is one user's public profile: GET /v1/users/profiles answers
// {profiles: [UserProfile]} for any id, never with more than the display
// name. DisplayName always has the default applied (store.DisplayName).
type UserProfile struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

// MyProfile is the caller's own profile (GET/PUT /v1/my/profile).
// DisplayName has the default applied; IsDefault reports that no name is
// set (so a settings form shows an empty field with the default as hint).
type MyProfile struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	IsDefault   bool   `json:"isDefault"`
}
