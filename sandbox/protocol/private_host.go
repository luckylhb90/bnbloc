package protocol

import (
	"net/netip"
	"strings"
)

// ValidPrivateHost is the one rule for a host a member lets the sandboxed
// runs of their own agents reach beside the public internet (the desktop's
// private-host list, egress public with rules under egressHosts;
// docs/prd/2026-10-21-sandbox-private-hosts.md): a lower-case DNS name of at
// least two labels, or an IP literal. Never a wildcard, a URL, a port,
// localhost, or a loopback, link-local (cloud metadata among them),
// unspecified, broadcast or multicast address. The desktop holds a copy
// (valid_private_host), pinned to this one by testdata/private_hosts.json.
func ValidPrivateHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.Zone() == "" && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() &&
			!ip.IsMulticast() && ip != netip.AddrFrom4([4]byte{255, 255, 255, 255})
	}
	labels := strings.Split(host, ".")
	if len(host) > 253 || len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	// A last label of digits only is a malformed address, and localhost is
	// the guest itself.
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != "" && last != "localhost"
}
