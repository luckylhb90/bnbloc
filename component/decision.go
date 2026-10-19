package component

// Bounds of a choice question of the decision API (POST /v1/systemone,
// docs/prd/2026-10-17-decision-models.md), which a component asking it
// through the room relay must hold to: the platform refuses a request with
// fewer or more choices.
const (
	MinDecisionChoices = 2
	MaxDecisionChoices = 255
)
