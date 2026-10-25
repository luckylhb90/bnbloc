package pulse

import "math"

// The v1 kinds (docs/prd/2026-10-24-executive-pulse-backend.md, section 3). A
// kind not listed here is refused; an attribute a kind does not name is dropped.
const (
	KindJobQueued       = "job.queued"
	KindJobClaimed      = "job.claimed"
	KindJobFinished     = "job.finished"
	KindJobSwept        = "job.swept"
	KindReplyUsage      = "reply.usage"
	KindArtifactVersion = "artifact.version"
	KindMessageSent     = "message.sent"
	KindAppWritten      = "app.written"
	KindPresenceSeen    = "presence.seen"
	KindPresenceLeft    = "presence.left"
	KindMemberJoined    = "member.joined"
)

// Closed value sets. A value outside its set is dropped, not guessed.
var (
	coreValues   = set("acp", "claude-code", "opencode", "codex", "other")
	statusValues = set("replied", "failed", "timed_out", "cancelled")
	// failureValues mirrors store.AgentJobFailureReason exactly (activity's drift test
	// checks every store reason is accepted here); "other" takes a future reason.
	failureValues = set("no_bridge_online", "agent_not_advertised", "borrow_not_authorized",
		"core_unavailable", "runner_start_failed", "runner_failed", "agent_error", "context_overflow",
		"context_reset", "lease_expired", "sandbox_starting", "reply_timeout", "cancelled",
		"budget_exhausted", "budget_unavailable", "sandbox_required", "content_unavailable",
		"team_unsupported", "invalid_job", "other")
	sweepValues   = set("no_bridge_online", "lease_expired", "other") // the sweep's own reasons
	channelValues = set("room", "agent")
	sourceValues  = set("manual", "directory")
)

type attrSpec struct {
	typ   attrType
	enums map[string]bool
}

// accepts reports whether a has the shape this attribute is declared with.
func (s attrSpec) accepts(a Attr) bool {
	if a.typ != s.typ {
		return false
	}
	switch s.typ {
	case typeEnum:
		return s.enums[a.s]
	case typeNum:
		return a.n >= 0 && !math.IsNaN(a.n) && !math.IsInf(a.n, 0)
	case typeFlag:
		return true
	case typeHash:
		return isHash(a.s)
	case typeZone:
		return a.s == "other" || validZone(a.s)
	case typeIdent:
		return validIdent(a.s)
	}
	return false
}

type kindSpec struct {
	attrs    map[string]attrSpec
	required []string
}

func enumAttr(values map[string]bool) attrSpec { return attrSpec{typ: typeEnum, enums: values} }

func numAttr() attrSpec   { return attrSpec{typ: typeNum} }
func flagAttr() attrSpec  { return attrSpec{typ: typeFlag} }
func hashAttr() attrSpec  { return attrSpec{typ: typeHash} }
func zoneAttr() attrSpec  { return attrSpec{typ: typeZone} }
func identAttr() attrSpec { return attrSpec{typ: typeIdent} }

// KnownFailureReason reports whether v is in the failure-reason set. A product
// maps any other reason to "other" before it builds an event.
func KnownFailureReason(v string) bool { return failureValues[v] }

// kinds is the whole schema. Nothing else may reach the queue.
var kinds = map[string]kindSpec{
	KindJobQueued: {
		attrs: map[string]attrSpec{
			"core": enumAttr(coreValues),
			"job":  hashAttr(),
		},
		required: []string{"core"},
	},
	KindJobClaimed: {
		attrs: map[string]attrSpec{
			"core":  enumAttr(coreValues),
			"phase": enumAttr(set("claimed")),
			"job":   hashAttr(),
		},
		required: []string{"core"},
	},
	KindJobFinished: {
		attrs: map[string]attrSpec{
			"core":          enumAttr(coreValues),
			"status":        enumAttr(statusValues),
			"failureReason": enumAttr(failureValues),
			"durationMs":    numAttr(),
			"job":           hashAttr(),
		},
		required: []string{"core", "status"},
	},
	KindJobSwept: {
		attrs: map[string]attrSpec{
			"core":   enumAttr(coreValues),
			"reason": enumAttr(sweepValues),
			"job":    hashAttr(),
		},
		required: []string{"reason"},
	},
	KindReplyUsage: {
		attrs: map[string]attrSpec{
			"core":      enumAttr(coreValues),
			"reported":  flagAttr(),
			"tokensIn":  numAttr(),
			"tokensOut": numAttr(),
			"costUsd":   numAttr(),
		},
		required: []string{"core", "reported"},
	},
	KindArtifactVersion: {
		attrs: map[string]attrSpec{
			"kind":   identAttr(),
			"isNew":  flagAttr(),
			"member": hashAttr(),
		},
		required: []string{"isNew"},
	},
	KindMessageSent: {
		attrs: map[string]attrSpec{
			"channelType": enumAttr(channelValues),
			"member":      hashAttr(),
		},
		required: []string{"channelType"},
	},
	KindAppWritten: {
		attrs: map[string]attrSpec{
			"public": flagAttr(),
			"member": hashAttr(),
		},
		required: []string{"public"},
	},
	KindPresenceSeen: {
		attrs: map[string]attrSpec{
			"member":  hashAttr(),
			"country": identAttr(),
		},
		required: []string{"member"},
	},
	KindPresenceLeft: {
		attrs: map[string]attrSpec{
			"member":  hashAttr(),
			"country": identAttr(),
		},
		required: []string{"member"},
	},
	KindMemberJoined: {
		attrs: map[string]attrSpec{
			"source": enumAttr(sourceValues),
		},
		required: []string{"source"},
	},
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// isHash reports whether s is a 64-character lowercase hex digest.
func isHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validZone accepts an IANA-style name: letters, digits and "_+-/", 1 to 40
// characters, no leading or trailing slash, no "..".
func validZone(s string) bool {
	if len(s) == 0 || len(s) > 40 || s[0] == '/' || s[len(s)-1] == '/' {
		return false
	}
	prev := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '+' || c == '-' || c == '/'
		if !ok || (c == '/' && prev == '/') {
			return false
		}
		prev = c
	}
	return true
}

// validIdent accepts 1 to 32 characters: a lowercase letter, then lowercase
// letters, digits, '_', '-' or '.'.
func validIdent(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}
