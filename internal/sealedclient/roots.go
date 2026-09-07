package sealedclient

// The verification roots, EMBEDDED and never fetched.
//
// These are PUBLIC keys. They ship in plaintext inside every binary and that is
// fine — what matters is that they are never DOWNLOADED. A client that fetches
// its own verification root can be handed a different one by exactly the
// network the root exists to distrust, which would make the entire signed
// keyset ceremony decorative.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// FleetRootKid names the fleet's root — the kid a managed stack's binding is
// signed under.
//
// THE FLEET'S root, not a stack's. An earlier value here was taken from a
// single dev stack, that stack was deleted, and because every tenant mints its
// own root the client could verify none of them (measured live 2026-08-24:
// every fetch refused). The private half of this key lives ONLY in the control
// plane: a tenant pod runs customer code, and a fleet key sitting there would
// let one customer mint keysets every other customer's client accepts.
//
// Confirmed against the iOS binding, which carries the same constant and the
// same reasoning: sdk/palbackend-ios-src/Sources/Palbe/Core/SealedKeyStore.swift:48-58.
const FleetRootKid = "sealed-root-v1"

const fleetRootPublicBase64 = "mRDTIWzhW0JCk0e2Jvkn679VeZBB38zOOfPTMO6vBpU="

// SelfHostRootKid is the kid a SELF-HOSTED stack registers its own root under.
//
// The same kid as the fleet's, deliberately: an app configured with a
// self-host root does not thereby trust the fleet, and one configured with
// neither does not accidentally trust a self-hoster. Because a map holds one
// key per kid, choosing one root EXCLUDES the other — which is what makes a
// silent fallback to the fleet root impossible rather than merely discouraged.
// `palsvc initenv` mints that root and hands its public half to the stack as
// PALBASE_SEALED_ROOT (v2/cmd/palsvc/initenv.go:160); the CLI receives it from
// `/v1/management/keys` as `sealed_root` and stores it per environment
// (internal/backend/app_environments.go:52).
const SelfHostRootKid = FleetRootKid

// ErrRootUnusable means a caller supplied a self-host root that is not an
// Ed25519 public key. It is an ERROR and never a downgrade to the fleet root:
// an operator who configured a root for their own stack and silently got the
// fleet's would be told their chain verified when it did not.
var ErrRootUnusable = errors.New("sealed: the configured sealing root is not an ed25519 public key")

// roots returns the single root this client verifies bindings against.
//
// selfHostRoot is base64 (std) as `/v1/management/keys` publishes it; empty
// means "this is a fleet stack" and yields the compiled-in fleet root.
func rootsFor(selfHostRoot string) (TrustedRoots, bool, error) {
	if s := strings.TrimSpace(selfHostRoot); s != "" {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, false, ErrRootUnusable
		}
		return TrustedRoots{SelfHostRootKid: ed25519.PublicKey(raw)}, true, nil
	}
	raw, err := base64.StdEncoding.DecodeString(fleetRootPublicBase64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		// Unreachable unless the constant above is edited wrongly, and a
		// compiled-in root that silently decodes to nothing would make every
		// verification fail with a signature error that names the wrong cause.
		return nil, false, fmt.Errorf("sealed: the compiled-in fleet root is unusable")
	}
	return TrustedRoots{FleetRootKid: ed25519.PublicKey(raw)}, false, nil
}

// ErrNoStackRef means the caller's configuration does not identify which stack
// it believes it is talking to, so the binding's subject check — the lock of
// the whole chain — has nothing to compare against.
var ErrNoStackRef = errors.New("sealed: cannot tell which stack this address is, so a fleet binding cannot be checked against it")

// expectedStackRef is which stack the CLI BELIEVES it is talking to.
//
// Mirrors SealedKeyStore.expectedStackRef() /
// SealedKeyStore.managedStackRef(of:) in the iOS binding — the two must agree
// or one of them refuses documents the other accepts. Both sources are LOCAL
// configuration (the API key we were handed and the address we chose to dial),
// never the untrusted document.
//
// A concrete ref inside the API key wins, including on a self-hosted address.
// Hosted `pb_project_…` keys instead name a shared namespace, and their
// fleet-signed subject is the deployment ref carried in the managed hostname.
func expectedStackRef(apiKey, baseURL string, selfHosted bool) (string, error) {
	parts := strings.SplitN(apiKey, "_", 3)
	if len(parts) != 3 || parts[0] != "pb" || parts[1] == "" || parts[2] == "" {
		return "", ErrNoStackRef
	}
	if parts[1] == "project" && !selfHosted {
		ref := managedStackRef(baseURL)
		if ref == "" {
			return "", ErrNoStackRef
		}
		return ref, nil
	}
	return parts[1], nil
}

// managedStackRef reads the deployment ref out of a fleet-owned hostname.
//
// Only the fleet's own DNS boundary may identify a hosted deployment. Subzones
// such as `<ref>.dev.palbase.studio` follow the same first-label routing
// contract; a lookalike suffix or a bare apex carries no ref at all.
func managedStackRef(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	const suffix = ".palbase.studio"
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	labels := strings.Split(strings.TrimSuffix(host, suffix), ".")
	for _, l := range labels {
		if l == "" {
			return ""
		}
	}
	return labels[0]
}
