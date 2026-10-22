// Package netguard decides which addresses the server's own outbound
// connections may reach, judged by the address actually dialled, after
// names are resolved, so a DNS name can't point a connection somewhere it
// was refused.
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

var (
	// Ranges that aren't on the public internet but that net/netip's
	// predicates leave out.
	sharedNAT = netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT, and tailnets
	thisNet   = netip.MustParsePrefix("0.0.0.0/8")
	benchmark = netip.MustParsePrefix("198.18.0.0/15")
	nat64     = netip.MustParsePrefix("64:ff9b::/96") // reaches IPv4 through a translator

	// Cloud instance metadata, which hands out the machine's credentials.
	// Most clouds serve it on a link-local address; these are the others.
	metadata = []netip.Addr{
		netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
		netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
	}
)

// Internal reports whether ip is anything but a public internet address:
// loopback, private, link-local, shared NAT, multicast and the like.
func Internal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if nat64.Contains(ip) {
		b := ip.As16()
		return Internal(netip.AddrFrom4([4]byte(b[12:])))
	}
	return !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) ||
		sharedNAT.Contains(ip) || thisNet.Contains(ip) || benchmark.Contains(ip)
}

// Metadata reports whether ip is where a cloud serves instance metadata,
// or link-local, where no storage or database a person means lives.
func Metadata(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLinkLocalUnicast() {
		return true
	}
	for _, m := range metadata {
		if ip == m {
			return true
		}
	}
	return false
}

// FromIP converts a net.IP, as the standard library hands them out.
func FromIP(ip net.IP) netip.Addr {
	a, _ := netip.AddrFromSlice(ip)
	return a.Unmap()
}

// Control returns a net.Dialer Control hook that refuses connections to
// addresses refuse matches. It sees every address a dial tries, so it
// holds for each address a name resolves to and for redirects alike.
func Control(refuse func(netip.Addr) bool) func(network, address string, _ syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%s: can't tell where that is", address)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("%s: can't tell where that is", address)
		}
		if refuse(ip.Unmap().WithZone("")) {
			return fmt.Errorf("%s points inside the network, which this server doesn't connect to", host)
		}
		return nil
	}
}
