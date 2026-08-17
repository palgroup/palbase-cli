package backend

// stack_sdk.go — the SDK a project's code is built against comes from the
// project.
//
// A backend is compiled against @palbase/backend and then RUN by a runtime that
// carries its own copy. If those two are different majors, the build produces
// something the runtime cannot execute — and the failure is a sentence about a
// missing function, three layers from the version that caused it. Measured on
// 2026-08-16: a real backend failed with "getRegisteredControllers is not a
// function", because the registry API it needs lives in the SDK the runtime
// carries and not in the one npm resolves.
//
// So the project hands over the bytes. `push` asks what the project runs, and
// installs from it when the project's own node_modules disagree — which makes
// the skew impossible rather than reported.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureProjectSDK installs the project's own SDK when the local one is a
// different MAJOR.
//
// Minor and patch differences are left alone: they are compatible by
// definition, and reinstalling on every push would replace a dependency the
// author chose for a reason.
func ensureProjectSDK(ctx context.Context, dir string, target Target, cred Credentials, w io.Writer) error {
	running, err := projectSDKVersion(ctx, target, cred)
	if err != nil || running == "" {
		// A project that will not say what it runs is not a reason to refuse a
		// push: the build below either works or fails with its own message.
		return nil
	}
	installed := installedSDKVersion(dir)
	if installed != "" && majorOf(installed) != 0 && majorOf(installed) == majorOf(running) {
		return nil
	}

	fmt.Fprintf(w, "this project runs @palbase/backend %s and this checkout has %s — installing the project's\n",
		running, orNone(installed))

	tarball, err := downloadProjectSDK(ctx, target, cred)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarball) }()

	cmd := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund", "--loglevel=error", tarball)
	cmd.Dir = dir
	if blob, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install the project's SDK: %s", strings.TrimSpace(trimBody(blob)))
	}
	fmt.Fprintf(w, "✓ @palbase/backend %s, from the project itself\n", running)
	return nil
}

// projectSDKVersion asks the project what its runtime is running.
func projectSDKVersion(ctx context.Context, target Target, cred Credentials) (string, error) {
	// The version travels on the well-known document, which is public and needs
	// no session: a checkout that is not signed in yet still has to be able to
	// see whether its SDK matches.
	described, err := describeStack(ctx, target.URL, target.Insecure)
	if err != nil {
		return "", err
	}
	return described.SDKVersion, nil
}

// downloadProjectSDK fetches the tarball and returns the file it wrote.
func downloadProjectSDK(ctx context.Context, target Target, cred Credentials) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(target.URL, "/")+"/v1/management/sdk", nil)
	if err != nil {
		return "", err
	}
	cred.Apply(req)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return "", fmt.Errorf("the project would not hand over its SDK (%d): %s", res.StatusCode, trimBody(body))
	}

	file, err := os.CreateTemp("", "palbase-backend-*.tgz")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(file, io.LimitReader(res.Body, 256<<20)); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

// installedSDKVersion reads what this checkout actually has, from the package
// itself rather than from package.json's RANGE — a range says what is allowed,
// and only the installed copy says what will be compiled against.
func installedSDKVersion(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "node_modules", "@palbase", "backend", "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}

func orNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}
