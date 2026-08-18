package backend

// lan.go — letting a phone reach the stack, deliberately.
//
// A local stack publishes on 127.0.0.1 and nothing else, and that is not
// timidity: it serves a real database password and a service_role key, and
// docker's own default would put both on whatever network the laptop is
// currently attached to. The dev compose has a test guarding exactly this.
//
// But 127.0.0.1 means THE PHONE when the request comes from a phone. The
// simulator shares the Mac's loopback and needs nothing; a device on the same
// wifi cannot reach the Mac at all — not because the address is wrong, but
// because the port is not listening on any interface it can see.
//
// So `--lan` exists and is opt-in, and only the HTTP port widens. Postgres stays
// on loopback in every mode: a phone needs the API, never the database.

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
)

// BindEnv is what the compose file reads for the HTTP publish address.
const BindEnv = "PALBASE_HTTP_BIND"

// lanAddress is the address a device on the same network would use to reach
// this machine: the IPv4 of the interface the default route leaves by.
//
// The default route rather than the first non-loopback address, because a
// developer machine has several — docker's bridge, a VPN, a virtual adapter —
// and only one of them is the one the phone is also on.
func lanAddress() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("this machine has no default route, so nothing on the network can reach it")
	}
	iface := ""
	for _, line := range strings.Split(string(out), "\n") {
		if _, rest, ok := strings.Cut(strings.TrimSpace(line), "interface:"); ok {
			iface = strings.TrimSpace(rest)
		}
	}
	if iface == "" {
		return "", fmt.Errorf("could not read the default route's interface")
	}
	addrs, err := interfaceIPv4(iface)
	if err != nil || addrs == "" {
		return "", fmt.Errorf("%s has no IPv4 address — is this machine on a network?", iface)
	}
	return addrs, nil
}

func interfaceIPv4(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() {
			return v4.String(), nil
		}
	}
	return "", nil
}

// announceLAN says what was just exposed, in the terms that matter.
//
// Not a warning nobody reads: it names the ONE thing that changed, the thing
// that did NOT, and how to undo it. A person who widened a port on a café
// network should be able to tell from this whether they mind.
func announceLAN(w io.Writer, url string) {
	fmt.Fprintf(w, "  reachable on this network at %s\n", url)
	fmt.Fprintln(w, "  anyone on this network can now reach this stack's API with its publishable key.")
	fmt.Fprintln(w, "  the database stays on 127.0.0.1, and `palbase start` without --lan puts the API back.")
}

// deviceSetupNotice is what an iOS app needs BEYOND the address.
//
// This used to end with "the simulator needs neither — it shares this machine's
// loopback". That was wrong, and measured wrong on 2026-08-18: ATS runs in the
// simulator too, and since iOS 17 it no longer allows cleartext to an IP
// ADDRESS by default, so NSAllowsLocalNetworking is needed there as well. The
// sample app carries the key already, which is why nobody noticed.
//
// The second key is a different gate and it is real: local network privacy
// (iOS 14+) makes an app ASK before it may open a connection to a LAN address
// at all. Apple's TN3179 says plainly that the simulator does not support it,
// so that one can only be exercised on a device.
//
// A certificate is not the alternative it looks like: with a locally-trusted CA
// an app needs no Info.plist key at all, but 19 of 19 ATS permutations fail
// against an UNtrusted root — there is no click-through in URLSession — so the
// cost moves into every client's trust store instead. Measured; see
// docs/paltimate/2026-08-18-envoy-everywhere/decisions.md D3.
func deviceSetupNotice(w io.Writer) {
	fmt.Fprintln(w, "  the app's Info.plist needs, on iOS 17 and later:")
	fmt.Fprintln(w, "    NSAppTransportSecurity → NSAllowsLocalNetworking = YES")
	fmt.Fprintln(w, "      (cleartext to an IP address is blocked without it — in the")
	fmt.Fprintln(w, "       Simulator as well, not only on a device)")
	fmt.Fprintln(w, "    NSLocalNetworkUsageDescription = \"<why your app talks to your Mac>\"")
	fmt.Fprintln(w, "      (a real device also shows the user a permission alert; the")
	fmt.Fprintln(w, "       Simulator cannot exercise that gate at all)")
}
