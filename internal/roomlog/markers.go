package roomlog

import "strings"

// The scaffolding of model-facing text, owned here and nowhere else: how a
// record line names who said what, and the marker that opens the message a
// turn answers. Every prompt composer, .room/ file and room tool output
// renders them through these functions.

// Sender is who an entry is from as an agent reads it: the speaker, or
// "jack/codex for maya" when an agent dispatched the message on the
// speaker's behalf (Entry.Via).
func Sender(e Entry) string {
	sp := strings.TrimSpace(e.Speaker)
	if sp == "" {
		sp = "unknown"
	}
	if via := strings.TrimSpace(e.Via); via != "" {
		sp = via + " for " + sp
	}
	return sp
}

// Line renders one record line: "maya: …", "maya → jack/codex: …" for a
// message addressed to an agent (Entry.To), "jack/codex for maya → jill/claude:
// …" for a task an agent dispatched on maya's behalf.
func Line(e Entry) string {
	head := Sender(e)
	if to := strings.TrimSpace(e.To); to != "" {
		head += " → " + to
	}
	return head + ": " + strings.TrimSpace(e.Text) + "\n"
}

// nowMarker opens the message a turn answers, naming who sent it (a Sender;
// "" when unknown).
func nowMarker(from string) string {
	if from = strings.TrimSpace(from); from == "" {
		return "[now]\n"
	}
	return "[now — from " + from + "]\n"
}
