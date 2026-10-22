package netguard

import (
	"net/netip"
	"testing"
)

func TestInternal(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.100.100.200",
		"0.0.0.0", "0.1.2.3", "198.18.0.1", "255.255.255.255", "224.0.0.1",
		"::1", "::", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe",
	} {
		if !Internal(netip.MustParseAddr(s)) {
			t.Errorf("Internal(%s) = false", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "100.128.0.1", "2606:4700:4700::1111", "64:ff9b::808:808"} {
		if Internal(netip.MustParseAddr(s)) {
			t.Errorf("Internal(%s) = true", s)
		}
	}
}

func TestMetadata(t *testing.T) {
	for _, s := range []string{"169.254.169.254", "169.254.170.2", "fe80::1", "fd00:ec2::254", "100.100.100.200", "::ffff:169.254.169.254"} {
		if !Metadata(netip.MustParseAddr(s)) {
			t.Errorf("Metadata(%s) = false", s)
		}
	}
	// Storage on this machine or a private network is fine.
	for _, s := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.10", "100.101.102.103", "1.1.1.1"} {
		if Metadata(netip.MustParseAddr(s)) {
			t.Errorf("Metadata(%s) = true", s)
		}
	}
}

func TestControl(t *testing.T) {
	c := Control(Metadata)
	if err := c("tcp4", "169.254.169.254:80", nil); err == nil {
		t.Error("metadata address allowed")
	}
	if err := c("tcp6", "[fe80::1%eth0]:80", nil); err == nil {
		t.Error("link-local address with a zone allowed")
	}
	if err := c("tcp4", "203.0.113.7:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}
