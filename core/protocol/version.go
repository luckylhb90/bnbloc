package protocol

// Version negotiation: not wire bodies (types.go is the wire contract), the
// one major-version rule every bnbloc HTTP protocol shares.

import (
	"fmt"
	"strconv"
	"strings"
)

type Version struct {
	Major int
	Minor int
}

func ParseVersion(raw string) (Version, error) {
	major, minor, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok || major == "" || minor == "" || strings.Contains(minor, ".") {
		return Version{}, fmt.Errorf("protocol version %q: expected major.minor", raw)
	}
	maj, err := strconv.Atoi(major)
	if err != nil || maj < 0 {
		return Version{}, fmt.Errorf("protocol version %q: invalid major", raw)
	}
	min, err := strconv.Atoi(minor)
	if err != nil || min < 0 {
		return Version{}, fmt.Errorf("protocol version %q: invalid minor", raw)
	}
	return Version{Major: maj, Minor: min}, nil
}

func Compatible(remote ProtocolRange) error {
	return CompatibleRange("core protocol", ProtocolRange{Min: SupportedMinVersion, Max: SupportedMaxVersion}, remote)
}

// CompatibleRange is the one major-version negotiation rule shared by every
// bnbloc HTTP protocol (Core Protocol, Sandbox Protocol): a peer is usable
// when its [min,max] range shares a major version with the local range.
// Minor versions are additive within a major, so they never break
// compatibility; optional behaviour is negotiated through capabilities.
// protocolName labels the mismatch error ("core protocol", "sandbox protocol").
func CompatibleRange(protocolName string, local, remote ProtocolRange) error {
	localMin, err := ParseVersion(local.Min)
	if err != nil {
		return err
	}
	localMax, err := ParseVersion(local.Max)
	if err != nil {
		return err
	}
	remoteMin, err := ParseVersion(remote.Min)
	if err != nil {
		return fmt.Errorf("remote min: %w", err)
	}
	remoteMax, err := ParseVersion(remote.Max)
	if err != nil {
		return fmt.Errorf("remote max: %w", err)
	}
	if remoteMax.Major < localMin.Major || remoteMin.Major > localMax.Major {
		return &VersionMismatchError{Protocol: protocolName, LocalMin: local.Min, LocalMax: local.Max, RemoteMin: remote.Min, RemoteMax: remote.Max}
	}
	return nil
}

type VersionMismatchError struct {
	// Protocol names the negotiated protocol; empty means "core protocol".
	Protocol  string
	LocalMin  string
	LocalMax  string
	RemoteMin string
	RemoteMax string
}

func (e *VersionMismatchError) Error() string {
	name := e.Protocol
	if name == "" {
		name = "core protocol"
	}
	peer := strings.TrimSuffix(name, " protocol")
	return fmt.Sprintf("%s version mismatch: bnbloc supports %s-%s, %s supports %s-%s", name, e.LocalMin, e.LocalMax, peer, e.RemoteMin, e.RemoteMax)
}
