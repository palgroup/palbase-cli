package backend

import "net"

// outboundLANIP returns the dev machine's primary LAN IPv4 address — the one a
// physical device on the same network uses to reach `palbase serve`. It opens a
// UDP socket toward a public address (no packet is sent; connect only selects
// the egress interface) and reads the kernel-chosen LocalAddr. On any failure
// (no route, odd VPN) it falls back to "localhost" so the simulator still works.
func outboundLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() {
		return "localhost"
	}
	if v4 := addr.IP.To4(); v4 != nil {
		return v4.String()
	}
	return addr.IP.String()
}
