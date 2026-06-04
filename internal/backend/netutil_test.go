package backend

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutboundLANIP_ReturnsParseableNonLoopback(t *testing.T) {
	ip := outboundLANIP()
	// On any machine with a network interface we expect a real IP. In a
	// fully offline sandbox the function falls back to "localhost"; accept
	// that explicitly so CI without a route still passes deterministically.
	if ip == "localhost" {
		t.Skip("no outbound route in this environment — fallback path taken")
	}
	parsed := net.ParseIP(ip)
	require.NotNil(t, parsed, "outboundLANIP must return a parseable IP, got %q", ip)
	require.False(t, parsed.IsLoopback(), "outboundLANIP must not return a loopback address, got %q", ip)
	require.NotNil(t, parsed.To4(), "expected an IPv4 LAN address, got %q", ip)
}
