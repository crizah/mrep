// Package netutil discovers the laptop's LAN-reachable IPv4 address so the
// QR code encodes a URL the phone can actually reach (HLD §5, §16).
package netutil

import (
	"errors"
	"net"
	"strings"
)

// ErrNoLANAddress is returned when no usable non-loopback IPv4 was found.
var ErrNoLANAddress = errors.New("no non-loopback IPv4 address found")

// virtualPrefixes are interface name prefixes that are never the right
// choice for a phone to dial into: container/VM bridges, VPN tunnels, etc.
// (HLD §16, "LAN IP discovery will pick the wrong interface").
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "tun", "tap", "wg", "vmnet", "vboxnet", "lo",
}

// LANIPv4 returns the best-guess LAN IPv4 address for this machine: the
// address of the interface carrying the default route if one can be
// determined, otherwise the first non-loopback, non-virtual IPv4 found on an
// active interface.
func LANIPv4() (string, error) {
	if ip, err := viaDefaultRoute(); err == nil {
		return ip, nil
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtual(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ip := ipv4Of(addr); ip != "" {
				return ip, nil
			}
		}
	}
	return "", ErrNoLANAddress
}

func isVirtual(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func ipv4Of(addr net.Addr) string {
	var ip net.IP
	switch v := addr.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return ""
	}
	ip = ip.To4()
	if ip == nil || ip.IsLoopback() {
		return ""
	}
	return ip.String()
}

// viaDefaultRoute finds the local IPv4 address that would be used to reach
// the public internet, without sending any packets: dialing UDP just asks
// the OS routing table to pick a source address. This is the most reliable
// way to skip docker0/virbr0/VPN interfaces that a plain interface scan can
// pick by accident.
func viaDefaultRoute() (string, error) {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsLoopback() {
		return "", ErrNoLANAddress
	}
	return local.IP.String(), nil
}
