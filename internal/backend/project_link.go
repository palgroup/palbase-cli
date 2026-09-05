package backend

// project_link.go — `palbase link <target>` : binding an app to a project.
//
// TARGET-RELATIVE, which is the rule the design settled on
// (docs/paltimate/2026-08-12-v2-faz0-selfhost/design-management-api.md §6, §10):
// a command that touches ONE tenant works against whatever target it is given,
// and only who authenticates changes. What you write decides which it is —
// something carrying a scheme is the target itself and no control plane is
// asked; a bare environment ref is resolved by ours.
//
// This is the direct half. `palbase ios link` still owns the ref half, because
// that one resolves a project in the cloud, registers an app and asks for an
// environment's key — none of which a project somebody runs has: it is one
// installation with one identity and one pair of keys in the .env beside it.
// The two meet when the management API exists on both sides; until then, the
// direct half is what makes a project you run usable at all.
//
// It writes the SAME slot files `link` writes, so everything after this point —
// `palbase spec`, the Swift generator, `palbe-gen` — behaves identically whether
// the stack is yours or ours. That is the point: one toolchain, two hosts.
import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// projectAppID names the app slot a linked checkout writes.
//
// A cloud project mints an app id when an app registers; a project somebody runs
// has no registry to mint one from, and inventing a value would put something in
// a committed file that means nothing and can never be looked up. The project's
// own name is the honest answer — and it matches the identity the stack boots
// with (`migrate.BootStackRef`), which is also what its API key carries.
const projectAppID = "project"

type linkOpts struct {
	url        string
	platforms  []string
	insecure   bool
	tokenStdin bool
}

func newLinkCmd(r Resolvers) *cobra.Command {
	var o linkOpts
	cmd := &cobra.Command{
		Use:   "link <target>",
		Args:  cobra.MaximumNArgs(1),
		Short: "Bind this app to a stack and generate its client",
		Long: `Bind this checkout to a project, and generate the typed client for it.

    palbase link http://localhost:54321      something running on this machine
    palbase link todoapp                     a project in the cloud

Linking is something you do AS SOMEBODY. Both the publishable key and the
contract come from the project over authenticated routes — the public document
says only what kind of thing answered — so a stranger who knows the address gets
neither. The credential comes from ` + "`palbase start`" + ` (which writes one for the
stack it just brought up), from ` + "`palbase login`" + `, or from PALBASE_ACCESS_TOKEN.

It writes:

  .palbase/project.json                     the project this checkout belongs to
  .palbase/<platform>/palbase-config.json   the app's URL + publishable key
  .palbase/openapi/<env>.json               the contract, one per environment
  Palbase/Generated/                        (apple) the committed Swift client

Run it again after every ` + "`palbase push`" + ` — or just ` + "`palbase spec`" + `, which
refreshes the contract alone, because that is the part that changed.

--insecure is for an address still using the self-signed certificate its first
boot generated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The target may be written as an argument or as --url; they are the
			// same thing, and refusing one of them would be a rule to remember
			// for no reason.
			if len(args) == 1 && o.url == "" {
				o.url = args[0]
			}
			// A BARE REF IS AN ADDRESS THIS CLOUD KNOWS, and the help above has
			// promised so all along: "palbase link <project> — a project in the
			// cloud". The code refused it and sent people to `palbase ios link`,
			// which links an iOS APP — no use at all to a backend checkout, which
			// then had no way to reach a cloud project by name. The only thing
			// missing was the suffix, and the configured cloud carries it.
			if o.url != "" && !strings.Contains(o.url, "://") {
				// ADI ÖNCE DENE. Yardım metni `palbase link todoapp` diyor ve
				// `project list` artık ADI ilk sütunda gösteriyor — insan orada
				// gördüğünü yazar. Ama bir ad çoğu zaman ref ŞEKLİNE de uyar
				// ("ioslinkprobe": 4-24 küçük harf), o yüzden şekil kontrolü tek
				// başına adı sessizce ref sanıyor ve var olmayan bir konağa
				// gidiyordu: "does not look like a Palbase stack" (ölçüldü
				// 25.08.2026). Belgelenmiş ama var olmayan bir davranıştı.
				//
				// Bulut oturumu yoksa ya da liste okunamazsa SESSİZCE ref yoluna
				// düşülür: self-host bir checkout'ta ad çözecek bir defter yok
				// ve orada ref/adres tek doğru cevaptır.
				if ref := refByProjectName(cmd.Context(), r, o.url); ref != "" {
					o.url = ref
				}
				if !selection.IsCanonicalEnvironmentRef(o.url) {
					return fmt.Errorf(
						"%q is neither a stack address nor an environment ref "+
							"(a ref is 4-24 lowercase letters and digits)", o.url)
				}
				host := r.Endpoints().PublicHost
				if host == "" {
					return fmt.Errorf("this CLI has no tenant host configured, so %q cannot be resolved to an address", o.url)
				}
				o.url = "https://" + o.url + "." + host
			}
			return runLink(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.url, "url", "", "the stack's base URL (e.g. https://127.0.0.1)")
	// NO DEFAULT: an empty list means "read the checkout", and a default of
	// `ios` meant `palbase link` in a web-only project wrote Apple artifacts and
	// nothing else — silently, because a wrong default looks exactly like a
	// right one. The flag stays for the case where somebody wants LESS than
	// what is here, not to make them repeat what is already true.
	f.StringSliceVar(&o.platforms, "platform", nil, "limit to ios, macos, android or web (default: whatever this checkout is)")
	f.BoolVar(&o.insecure, "insecure", false, "accept the stack's self-signed certificate")
	f.BoolVar(&o.tokenStdin, "token-stdin", false,
		"read this stack's key from stdin and remember it for this address (self-hosted stacks)")
	return cmd
}

// refByProjectName, çağıranın projeleri arasında ADI eşleşenin ref'ini verir.
//
// Boş dönmesi "bulamadım" demektir ve çağıran ref yoluna düşer — bir hata
// DEĞİL: self-host bir checkout'ta ad çözecek bir defter yoktur.
//
// TAM ve büyük/küçük harf duyarsız eşleşme; birden fazla eşleşirse boş döner,
// çünkü hangisinin kastedildiğini bilmiyoruz ve birini seçmek yanlış projeye
// bağlanmak olabilir.
func refByProjectName(ctx context.Context, r Resolvers, name string) string {
	if r.REST == nil {
		return ""
	}
	rest := r.REST()
	if rest == nil {
		return ""
	}
	// Şekli BURADA beyan et: `internal/project` paketini içeri almak, backend
	// paketini komut ağacının bir yaprağına bağlardı ve döngü üretirdi.
	var rows []struct {
		Ref  string  `json:"ref"`
		Name *string `json:"name"`
	}
	if err := rest.Do(ctx, http.MethodGet, "/v1/cloud/projects", nil, &rows); err != nil {
		return ""
	}
	found := ""
	for _, p := range rows {
		if p.Name == nil || !strings.EqualFold(strings.TrimSpace(*p.Name), strings.TrimSpace(name)) {
			continue
		}
		if found != "" {
			// İki proje aynı adı taşıyor: seçmek yanlış projeye bağlanmak olabilir.
			return ""
		}
		found = p.Ref
	}
	return found
}

// knownRefs lists the refs this account can see, and says whether it could ask
// at all. The boolean is the whole point: a listing that FAILED must not be read
// as "no such project" — that is how a network blip becomes a lie about what
// exists. Callers only refuse when the answer is known and the ref is not in it.
func knownRefs(ctx context.Context, r Resolvers) ([]string, bool) {
	if r.REST == nil {
		return nil, false
	}
	rest := r.REST()
	if rest == nil {
		return nil, false
	}
	var rows []struct {
		Ref string `json:"ref"`
	}
	if err := rest.Do(ctx, http.MethodGet, "/v1/cloud/projects", nil, &rows); err != nil {
		return nil, false
	}
	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Ref != "" {
			refs = append(refs, row.Ref)
		}
	}
	return refs, true
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	base := strings.TrimRight(strings.TrimSpace(o.url), "/")
	if base == "" {
		// A BOUND CHECKOUT ALREADY NAMES ITS ADDRESS. Re-linking is how you
		// refresh generated clients after a contract change, and demanding the
		// URL again asks the reader to retype what the committed file says —
		// which is also how the two drift apart.
		if target, err := ReadTarget(); err == nil && strings.TrimSpace(target.URL) != "" {
			base = strings.TrimRight(strings.TrimSpace(target.URL), "/")
		}
	}
	if base == "" {
		return errors.New("--url is required: the address the stack serves on")
	}
	// What is at this address, and does it answer at all. One request, no
	// credential: the document says WHAT answered and which SDK it runs, and a
	// project that does not answer is either not a Palbase project or not up.
	described, err := describeStack(ctx, base, o.insecure)
	if err != nil {
		return err
	}

	// A SELF-HOSTED STACK IS TOLD WHO YOU ARE, HERE.
	//
	// Nothing else can say: it is not on this machine and it is not in our
	// ledger. The key goes in through stdin rather than an argument so it stays
	// out of the shell's history, is checked against the stack before anything
	// is written, and is then remembered for this address — so `push`, `spec`,
	// `secret` and the rest need no environment variable.
	if o.tokenStdin {
		token, err := readTokenFrom(os.Stdin)
		if err != nil {
			return err
		}
		if err := storeVerifiedToken(ctx, Target{URL: base, Insecure: o.insecure}, token); err != nil {
			return err
		}
		fmt.Fprintf(w, "remembered this stack's key for %s\n", base)
	}

	// The publishable key comes from the project, over an authenticated route.
	// It used to ride on the public document, which meant anyone who knew the
	// address was handed a working client credential; now linking is something
	// you do as somebody.
	target := Target{URL: base, Insecure: o.insecure}
	anon, err := projectPublishableKey(ctx, target)
	if err != nil {
		return err
	}

	// Remember the target. `login`, `push` and `spec` read it, so none of them
	// asks for an address again — and a colleague who clones this repository
	// reaches the same stack without being told which one it is.
	if err := WriteTarget(target); err != nil {
		return err
	}

	// EVERY environment, not the one being linked. An app that holds only the
	// environment somebody linked last is an app whose address depends on when
	// it was built — which is how a TestFlight build ends up pointed at staging.
	envs, specs, err := gatherEnvironments(ctx, target, anon, w)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", nativeArtifactsDir, err)
	}
	// EACH PLATFORM GETS WHAT ITS OWN GENERATOR READS.
	//
	// This loop used to write one document for all of them and then run the
	// Apple steps unconditionally, so `--platform web` (and `--platform
	// android`) produced an Xcode build configuration, a Swift client, and a
	// config in the native shape sitting at the WEB generator's path. Measured
	// on 2026-08-25 in the palaicloud checkout: `palbase web link` — which
	// lands here whenever the checkout is bound to a target — wrote
	// `Palbase/Config/Main.xcconfig`, overwrote `Palbase/palbase-config.json`
	// with the environment map, and never wrote `Palbase/openapi.json`. Not one
	// step failed; `palbe-gen` simply had nothing it could read.
	//
	// The platform list is not decoration: it says which toolchain is on the
	// other end, and a link that ignores it is a link for one platform wearing
	// a flag for four.
	// WHAT THIS CHECKOUT IS, unless the caller asked for less.
	if err := validatePlatforms(o.platforms); err != nil {
		return err
	}
	platforms := o.platforms
	if len(platforms) == 0 {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		platforms = detectPlatforms(root)
		if len(platforms) == 0 {
			return fmt.Errorf(
				"cannot tell what kind of app this is: looked for an Xcode project or workspace, "+
					"an Android applicationId in build.gradle[.kts], and a package.json beside an "+
					"index.html/public/src/app — found none in %s.\n  Name it with --platform if this "+
					"checkout keeps them somewhere else", root)
		}
		fmt.Fprintf(w, "▸ %s\n", strings.Join(platforms, ", "))
	}

	apple := false
	for _, platform := range platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		var (
			path string
			err  error
		)
		if platform == webPlatform {
			path, err = writeWebArtifacts(envs, specs, w)
		} else {
			path, err = writeAppEnvironments(platform, envs)
		}
		if err != nil {
			return err
		}
		if isApplePlatform(platform) {
			apple = true
		}
		written := strings.Join(envs.names(), ", ")
		if platform == webPlatform {
			// The flat document carries ONE environment, so naming the others
			// here would describe a file that does not contain them.
			written = envs.Default
		}
		fmt.Fprintf(w, "wrote %s (%s)\n", path, written)
	}

	if apple {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := writeXcconfigs(root, envs, w); err != nil {
			return err
		}
		reportInfoPlistRequirement(root, envs, w)
	}

	fmt.Fprintf(w, "\nlinked to %s (%s)\n", base, described.Hosting)

	// The contract, once per environment: they can differ, and a client merged
	// across them would compile calls that do not exist where the app points.
	// Swift only — the web client is generated by `palbe-gen`, which ships in
	// @palbase/web and reads the committed artifacts offline.
	if apple {
		if err := generateForEnvironments(ctx, envs, w); err != nil {
			return err
		}
	}
	reportContractDrift(specs, w)

	if apple {
		fmt.Fprintln(w, "commit .palbase/ and Palbase/Generated/")
	} else {
		fmt.Fprintln(w, "commit .palbase/ and Palbase/")
	}
	return nil
}

// webPlatform is the one platform whose generator lives in an SDK rather than
// in this CLI, and whose committed config is flat rather than per-environment.
const webPlatform = "web"

// knownPlatforms is the closed set `link` can actually build for.
var knownPlatforms = []string{"ios", "macos", "android", webPlatform}

// validatePlatforms refuses a value this CLI cannot build for, BY NAME.
//
// `--platform bogus` used to sail straight through: the loop below simply never
// matched it, so the run wrote nothing for that entry and said nothing about it.
// A flag that accepts anything teaches the reader that their typo worked, and
// they find out later — from a missing file rather than from the flag.
func validatePlatforms(platforms []string) error {
	for _, p := range platforms {
		name := strings.ToLower(strings.TrimSpace(p))
		if !slices.Contains(knownPlatforms, name) {
			return fmt.Errorf("%q is not a platform this can link: choose from %s",
				p, strings.Join(knownPlatforms, ", "))
		}
	}
	return nil
}

// runUnlink removes the bond, and the bond is the committed project file.
//
// The old `web unlink` deleted the SELECTION — a second addressing mechanism on
// its way out — and pointed the reader at `palbase web link`, a command on its
// way out too. What makes a checkout linked is .palbase/project.json.
//
// AN ALREADY-UNLINKED CHECKOUT IS NOT AN ERROR: the caller asked to end up
// somewhere, and it is already there.
func runUnlink(w io.Writer) error {
	path := projectPath()
	switch err := os.Remove(path); {
	case err == nil:
		fmt.Fprintf(w, "✓ unlinked — removed %s\n", path)
	case os.IsNotExist(err):
		fmt.Fprintln(w, "this checkout was not linked")
		return nil
	default:
		return fmt.Errorf("remove %s: %w", path, err)
	}
	// Remove .palbase/ when nothing else lives there.
	if entries, err := os.ReadDir(nativeArtifactsDir); err == nil && len(entries) == 0 {
		_ = os.Remove(nativeArtifactsDir)
	}
	fmt.Fprintln(w, "  generated clients and their imports are left in place")
	fmt.Fprintln(w, "  re-link with `palbase link <url>`")
	return nil
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Args:  cobra.NoArgs,
		Short: "Detach this checkout from its Palbase project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUnlink(cmd.OutOrStdout())
		},
	}
}

// isApplePlatform reports whether a platform is built by Xcode — the only ones
// an xcconfig, an Info.plist requirement, or a Swift client mean anything to.
func isApplePlatform(platform string) bool {
	return platform == "ios" || platform == "macos"
}

// describeStack asks a stack what it is.
//
// One request, no credential: the document is public because everything in it
// is (see the stack's internal/server/wellknown.go). A stack that does not
// answer is either not a Palbase stack or NOT UP YET, and saying which is the
// whole value of asking first — so the two are now actually told apart.
//
// They used to be collapsed into one sentence, and the sentence was the wrong
// one: a project created seconds earlier answers 503 from the cell's edge while
// it comes up, and `palbase link` told the person who had just created it that
// their address "does not look like a Palbase stack". The comment above already
// promised this distinction; only the code was missing it.
func describeStack(ctx context.Context, base string, insecure bool) (stackDescription, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if insecure {
		client.Transport = insecureTransport()
	}
	status, body, err := sendWaitingForReady(ctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, base+wellKnownPath, nil)
	}, os.Stderr, stackReadyWait, stackReadyRetryEvery)
	if err != nil {
		return stackDescription{}, fmt.Errorf(
			"reach %s: %w\n(a self-signed certificate needs --insecure)", base, err)
	}
	if status == http.StatusServiceUnavailable {
		return stackDescription{}, fmt.Errorf(
			"%s did not start serving within %s — it is a Palbase address that is not up yet, "+
				"so try again rather than changing anything", base, stackReadyWait)
	}
	if status != http.StatusOK {
		return stackDescription{}, fmt.Errorf(
			"%s does not look like a Palbase stack: %s answered %d", base, wellKnownPath, status)
	}
	var described stackDescription
	if err := json.Unmarshal(body, &described); err != nil {
		return stackDescription{}, fmt.Errorf("%s answered something unexpected: %s", wellKnownPath, trimBody(body))
	}
	if described.Hosting == "" {
		return stackDescription{}, fmt.Errorf(
			"%s answered %s without saying what it is", base, wellKnownPath)
	}
	return described, nil
}

// wellKnownPath is where a stack describes itself.
const wellKnownPath = "/.well-known/palbase.json"

// stackDescription is that document. It mirrors the stack's own struct; the two
// are small and the contract between them is three fields, so this is written
// out rather than generated.
type stackDescription struct {
	Hosting    string `json:"hosting"`
	SDKVersion string `json:"sdk_version"`
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// insecureTransport accepts a certificate nobody issued.
//
// It exists for exactly one case: a stack still using the self-signed pair its
// first boot generated, being linked from the machine that runs it. It is opt-in
// per invocation (`--insecure`), never a default and never inferred from the
// host — a tool that silently stopped verifying certificates for "local-looking"
// addresses would be a tool that stops verifying them.
func insecureTransport() http.RoundTripper {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in, documented above
}
