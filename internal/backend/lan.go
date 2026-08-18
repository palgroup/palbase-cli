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
	fmt.Fprintln(w, "  the database is not on the host at all — only the edge publishes — and")
	fmt.Fprintln(w, "  `palbase start` without --lan puts the API back on 127.0.0.1.")
}

// deviceSetupNotice is what an iOS app needs BEYOND the address, and every line
// of it was MEASURED on 2026-08-18 rather than reasoned about.
//
// The measurement, on an iOS 26.5 Simulator, with a bundle whose Info.plist
// carries no ATS keys at all and one that carries NSAllowsLocalNetworking:
//
//	http://192.168.1.119:65305/health   200, BOTH bundles
//	http://neverssl.com/                -1022 ATS, BOTH bundles
//
// So ATS is switched on and enforced — the public-host refusal proves the probe
// was subject to it — and it does NOT stand between an app and a cleartext
// address on the private network. The key is what Apple documents for `.local`
// names; for a LAN IP the Simulator needed nothing.
//
// This comment has now been wrong in both directions, which is why the numbers
// are here. It first said "the simulator needs neither — it shares this
// machine's loopback": right conclusion, wrong reason (a LAN address is not
// loopback). It was then "corrected" to say the Simulator needs
// NSAllowsLocalNetworking too, from a reading of the iOS 17 ATS notes and no
// measurement — which the probe above refutes.
//
// A real DEVICE is a different gate and it is not ATS: local network privacy
// (iOS 14+) makes the system ASK the person before an app may open a connection
// to a LAN address, and it reads NSLocalNetworkUsageDescription to say why.
// Apple's TN3179 states the Simulator does not support local network privacy,
// so that half cannot be measured here and is reported as what it is.
//
// A certificate is not the alternative it looks like: with a locally-trusted CA
// an app needs no Info.plist key either, but 19 of 19 ATS permutations fail
// against an UNtrusted root — there is no click-through in URLSession — so the
// cost moves into every client's trust store instead. Measured; see
// docs/paltimate/2026-08-18-envoy-everywhere/decisions.md D3.
func deviceSetupNotice(w io.Writer) {
	fmt.Fprintln(w, "  from the Simulator this address needs no app configuration:")
	fmt.Fprintln(w, "    cleartext to a private LAN address is allowed (measured on iOS 26.5);")
	fmt.Fprintln(w, "    it is cleartext to a PUBLIC host that ATS refuses, key or no key.")
	fmt.Fprintln(w, "  on a real device, add to the app's own Info.plist:")
	fmt.Fprintln(w, "    NSLocalNetworkUsageDescription = \"<why your app talks to your Mac>\"")
	fmt.Fprintln(w, "      (local network privacy asks the person once; the Simulator")
	fmt.Fprintln(w, "       cannot exercise that gate at all — Apple TN3179)")
}
