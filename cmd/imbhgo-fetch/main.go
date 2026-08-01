// Command imbhgo-fetch downloads the prebuilt combined static library (libimbhgo.a) for the current
// platform so a consumer can build github.com/moriyoshi/imbh-go WITHOUT building the Rust side.
//
// It resolves the target cell (GOOS/GOARCH, plus glibc vs musl on Linux), downloads the matching
// compressed asset from the release, verifies its SHA-256 against the release's SHA256SUMS manifest,
// decompresses it into a per-cell cache directory, and prints the CGO_LDFLAGS the go build needs.
//
// Typical use (no Rust toolchain required):
//
//	go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@v0.2.0
//	eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@v0.2.0 -print-env)"
//	go build -tags sable_extern_lib ./...
//
// -print-env emits POSIX shell syntax on every platform; pass -shell cmd or -shell powershell to
// get the native Windows form instead.
//
// This program is intentionally free of cgo and of any dependency on the imbhgo package itself, so
// it builds and runs before the archive it fetches exists.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/moriyoshi/imbh-go/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "imbhgo-fetch: "+err.Error())
		os.Exit(1)
	}
}

type options struct {
	version  string
	goos     string
	goarch   string
	libc     string
	dest     string
	baseURL  string
	printEnv bool
	shell    string
	force    bool
}

// Shell dialects -print-env can emit. The default is POSIX: the documented contract is
// `eval "$(imbhgo-fetch -print-env)"`, and on Windows CI that shell is git-bash/MSYS2 (what the
// GitHub Actions `bash` shell runs), not cmd.exe.
const (
	shellSh         = "sh"
	shellCmd        = "cmd"
	shellPowerShell = "powershell"
)

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("imbhgo-fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.version, "version", resolveVersion(), "release version to fetch (e.g. v0.2.0)")
	fs.StringVar(&o.goos, "os", runtime.GOOS, "target GOOS")
	fs.StringVar(&o.goarch, "arch", runtime.GOARCH, "target GOARCH")
	fs.StringVar(&o.libc, "libc", "", "Linux C library: glibc or musl (default: auto-detect)")
	fs.StringVar(&o.dest, "dest", "", "destination directory for libimbhgo.a (default: user cache dir)")
	fs.StringVar(&o.baseURL, "base-url", release.DefaultBaseURL, "release download base URL")
	fs.BoolVar(&o.printEnv, "print-env", false, "print only the CGO_LDFLAGS export line on stdout (for eval)")
	fs.StringVar(&o.shell, "shell", shellSh, "shell dialect for -print-env: sh, cmd, or powershell")
	fs.BoolVar(&o.force, "force", false, "re-download even if a matching archive is already cached")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch o.shell {
	case shellSh, shellCmd, shellPowerShell:
	default:
		return fmt.Errorf("unknown -shell %q (want %s, %s, or %s)", o.shell, shellSh, shellCmd, shellPowerShell)
	}
	if o.libc == "" {
		o.libc = release.DefaultLibc(o.goos, osStatExists)
	}
	return fetch(o, stdout, stderr)
}

// resolveVersion prefers the version stamped into the module when this tool is `go run`/installed at
// a tagged version (…/cmd/imbhgo-fetch@v0.2.0); it falls back to the compiled-in release.Version for
// local/devel builds where the build info carries no concrete version.
func resolveVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return release.Version
}

// httpClient is the client used for all downloads; a modest timeout keeps CI/interactive runs from
// hanging on a stalled connection.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

func fetch(o options, stdout, stderr io.Writer) error {
	subdir := release.CacheSubdir(o.version, o.goos, o.goarch, o.libc)
	cacheDir := o.dest
	if cacheDir == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("locate cache dir: %w", err)
		}
		cacheDir = filepath.Join(root, filepath.FromSlash(subdir))
	}
	archivePath := filepath.Join(cacheDir, "libimbhgo.a")
	sidecarPath := archivePath + ".sha256"

	assetURL := release.AssetURL(o.baseURL, o.version, o.goos, o.goarch, o.libc)
	assetName := release.AssetName(o.version, o.goos, o.goarch, o.libc)

	logf(stderr, "target %s/%s%s, version %s", o.goos, o.goarch, libcSuffix(o.libc), o.version)

	expected, err := expectedSum(o.baseURL, o.version, assetName)
	if err != nil {
		// Offline or no manifest: fall back to an existing cached archive if present.
		if !o.force {
			if _, statErr := os.Stat(archivePath); statErr == nil {
				logf(stderr, "checksum manifest unavailable (%v); using cached %s", err, archivePath)
				return emit(o, stdout, stderr, cacheDir)
			}
		}
		return fmt.Errorf("fetch checksum manifest: %w", err)
	}

	if !o.force && cachedMatches(archivePath, sidecarPath, expected) {
		logf(stderr, "up to date: %s", archivePath)
		return emit(o, stdout, stderr, cacheDir)
	}

	logf(stderr, "downloading %s", assetURL)
	compressed, err := download(assetURL)
	if err != nil {
		return err
	}
	if got := sha256Hex(compressed); got != expected {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, expected)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	if err := decompressToFile(compressed, archivePath); err != nil {
		return err
	}
	if err := os.WriteFile(sidecarPath, []byte(expected+"\n"), 0o644); err != nil {
		return fmt.Errorf("write checksum sidecar: %w", err)
	}
	logf(stderr, "installed %s", archivePath)
	return emit(o, stdout, stderr, cacheDir)
}

// expectedSum downloads the SHA256SUMS manifest and returns the hex checksum recorded for assetName.
func expectedSum(baseURL, version, assetName string) (string, error) {
	body, err := download(release.SumsURL(baseURL, version))
	if err != nil {
		return "", err
	}
	sum, ok := parseSums(string(body), assetName)
	if !ok {
		return "", fmt.Errorf("no checksum for %s in %s", assetName, release.SumsName)
	}
	return sum, nil
}

// parseSums finds the hex checksum for assetName in a coreutils sha256sum manifest
// ("<hex>  <filename>" or "<hex> *<filename>" per line). The filename may carry a path prefix.
func parseSums(manifest, assetName string) (string, bool) {
	for _, line := range strings.Split(manifest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if filepath.Base(name) == assetName {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func cachedMatches(archivePath, sidecarPath, expected string) bool {
	if _, err := os.Stat(archivePath); err != nil {
		return false
	}
	got, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(got)) == expected
}

func download(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// decompressToFile zstd-decompresses src into dst, writing to a temp file and renaming for atomicity.
func decompressToFile(src []byte, dst string) error {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return fmt.Errorf("init zstd: %w", err)
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(src, nil)
	if err != nil {
		return fmt.Errorf("zstd decode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".libimbhgo-*.a.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("install archive: %w", err)
	}
	return nil
}

// emit prints the CGO_LDFLAGS the consumer's `go build -tags sable_extern_lib` needs. With
// -print-env only the assignment line goes to stdout (so `eval "$(...)"` works); otherwise a
// human-readable summary goes to stderr.
//
// The dialect comes from -shell, never from the target GOOS: what consumes the line is the shell
// this tool was invoked from, and on Windows that is usually POSIX (git-bash / MSYS2). Deriving it
// from GOOS emitted cmd.exe's `set VAR=…` into bash, where it sets positional parameters instead of
// an environment variable — the consumer then built with an empty CGO_LDFLAGS.
func emit(o options, stdout, stderr io.Writer, cacheDir string) error {
	ldflags := ldflagsFor(cacheDir)
	if o.printEnv {
		fmt.Fprintln(stdout, envLine(o.shell, "CGO_LDFLAGS", ldflags))
		logf(stderr, "remember to build with -tags sable_extern_lib")
		return nil
	}
	logf(stderr, "set CGO_LDFLAGS=%q and build with -tags sable_extern_lib", ldflags)
	logf(stderr, "e.g.: CGO_LDFLAGS=%q go build -tags sable_extern_lib ./...", ldflags)
	return nil
}

// ldflagsFor builds the -L/-l pair for cacheDir. cmd/go splits CGO_LDFLAGS into fields itself and
// only honours a quote that opens a whole field, so a directory containing a space (routine under a
// Windows user profile) must be quoted as one complete -L argument, not just around the path.
func ldflagsFor(cacheDir string) string {
	search := "-L" + cacheDir
	if strings.ContainsAny(search, " \t") {
		search = "'" + search + "'"
	}
	return search + " -limbhgo"
}

// envLine renders `name=value` in the requested shell dialect. shell has already been validated by
// run; an unknown value falls back to POSIX rather than emitting nothing.
func envLine(shell, name, value string) string {
	switch shell {
	case shellCmd:
		// cmd.exe takes the rest of the line literally, quotes included; no escaping applies.
		return fmt.Sprintf("set %s=%s", name, value)
	case shellPowerShell:
		return fmt.Sprintf("$env:%s = %s", name, powerShellQuote(value))
	default:
		return fmt.Sprintf("export %s=%s", name, posixQuote(value))
	}
}

// posixQuote single-quotes s for a POSIX shell, so Windows backslashes, spaces and $ survive
// verbatim through `eval`.
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// powerShellQuote single-quotes s for PowerShell, where a literal quote is doubled.
func powerShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func libcSuffix(libc string) string {
	if libc == release.LibcMusl {
		return " (musl)"
	}
	return ""
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// osStatExists reports whether any path matches the glob (used for musl loader detection).
func osStatExists(glob string) bool {
	matches, err := filepath.Glob(glob)
	return err == nil && len(matches) > 0
}

func logf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "imbhgo-fetch: "+format+"\n", args...)
}
