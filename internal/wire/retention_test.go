package wire

import (
	"encoding/json"
	"testing"
)

func days(n int) *int { return &n }

func TestRetentionValidate(t *testing.T) {
	ok := []RetentionPolicy{
		{},
		{MessagesDays: days(0)},
		{MessagesDays: days(RetentionMinDays), AuditDays: days(RetentionMaxDays)},
		{ArtifactVersionsDays: days(90), AgentJobsDays: days(365)},
	}
	for _, p := range ok {
		if err := p.Validate(); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	bad := []RetentionPolicy{
		{MessagesDays: days(-1)},
		{AuditDays: days(1)},
		{AgentJobsDays: days(RetentionMinDays - 1)},
		{ArtifactVersionsDays: days(RetentionMaxDays + 1)},
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Fatalf("%+v: want an error", p)
		}
	}
}

// The tenant override wins where set (0 keeps forever even under a platform
// default); null falls back to the platform default; the platform default
// stores 0 as null.
func TestEffectiveRetention(t *testing.T) {
	platform := RetentionPolicy{MessagesDays: days(0), AuditDays: days(365), AgentJobsDays: days(90)}.AsDefault()
	if platform.MessagesDays != nil || *platform.AuditDays != 365 {
		t.Fatalf("AsDefault = %+v", platform)
	}
	override := RetentionPolicy{AuditDays: days(0), ArtifactVersionsDays: days(60)}
	got := EffectiveRetention(platform, override)
	if got.MessagesDays != nil || got.AuditDays != nil {
		t.Fatalf("keep forever must resolve to null: %+v", got)
	}
	if got.ArtifactVersionsDays == nil || *got.ArtifactVersionsDays != 60 || got.AgentJobsDays == nil || *got.AgentJobsDays != 90 {
		t.Fatalf("effective = %+v", got)
	}
	// Nulls stay explicit on the wire so a client can tell "unset" apart.
	raw, _ := json.Marshal(RetentionPolicy{})
	if string(raw) != `{"messagesDays":null,"artifactVersionsDays":null,"auditDays":null,"agentJobsDays":null}` {
		t.Fatalf("json = %s", raw)
	}
}

// A channel only shortens the organization's retention (C22, E2): the
// shorter applies; no channel setting follows the organization; neither
// keeps forever.
func TestEffectiveChannelRetention(t *testing.T) {
	d := func(n int) *int { return &n }
	val := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	for _, c := range []struct {
		channel int
		org     *int
		want    int
	}{
		{0, nil, -1},
		{0, d(90), 90},
		{7, nil, 7},
		{7, d(90), 7},
		{120, d(90), 90},
		{30, d(0), 30},
	} {
		if got := val(EffectiveChannelRetention(c.channel, c.org)); got != c.want {
			t.Errorf("EffectiveChannelRetention(%d, %v) = %d, want %d", c.channel, val(c.org), got, c.want)
		}
	}
}
