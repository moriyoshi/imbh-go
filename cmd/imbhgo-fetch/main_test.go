package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/moriyoshi/imbh-go/internal/release"
)

// fakeRelease serves a single cell's compressed archive and a SHA256SUMS manifest, mimicking the
// GitHub Releases layout <base>/<version>/<asset>. It records how many times the asset was fetched
// so tests can assert the cache-hit path performs no download.
type fakeRelease struct {
	version    string
	goos       string
	goarch     string
	libc       string
	rawArchive []byte
	compressed []byte
	assetHits  int
}

func newFakeRelease(t *testing.T, raw []byte, corruptSums bool) (*fakeRelease, *httptest.Server) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	comp := enc.EncodeAll(raw, nil)
	enc.Close()

	fr := &fakeRelease{
		version: "v9.9.9", goos: "linux", goarch: "amd64", libc: release.LibcGlibc,
		rawArchive: raw, compressed: comp,
	}
	assetName := release.AssetName(fr.version, fr.goos, fr.goarch, fr.libc)

	sum := sha256.Sum256(comp)
	sumHex := hex.EncodeToString(sum[:])
	if corruptSums {
		sumHex = strings.Repeat("0", 64)
	}
	sums := fmt.Sprintf("%s  %s\n", sumHex, assetName)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+fr.version+"/"+release.SumsName, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sums))
	})
	mux.HandleFunc("/"+fr.version+"/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		fr.assetHits++
		w.Write(comp)
	})
	return fr, httptest.NewServer(mux)
}

func baseOpts(fr *fakeRelease, srv *httptest.Server, dest string) options {
	return options{
		version: fr.version, goos: fr.goos, goarch: fr.goarch, libc: fr.libc,
		dest: dest, baseURL: srv.URL,
	}
}

func TestFetchDownloadsVerifiesDecompresses(t *testing.T) {
	raw := []byte("!<arch>\nfake libimbhgo.a payload\n")
	fr, srv := newFakeRelease(t, raw, false)
	defer srv.Close()
	dest := t.TempDir()

	var out, errBuf bytes.Buffer
	if err := fetch(baseOpts(fr, srv, dest), &out, &errBuf); err != nil {
		t.Fatalf("fetch: %v\nstderr:\n%s", err, errBuf.String())
	}

	got, err := os.ReadFile(filepath.Join(dest, "libimbhgo.a"))
	if err != nil {
		t.Fatalf("read installed archive: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("installed archive mismatch: got %q want %q", got, raw)
	}
	if fr.assetHits != 1 {
		t.Fatalf("expected exactly one asset download, got %d", fr.assetHits)
	}
}

func TestFetchCacheHitSkipsDownload(t *testing.T) {
	raw := []byte("cached payload")
	fr, srv := newFakeRelease(t, raw, false)
	defer srv.Close()
	dest := t.TempDir()

	var out, errBuf bytes.Buffer
	if err := fetch(baseOpts(fr, srv, dest), &out, &errBuf); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if fr.assetHits != 1 {
		t.Fatalf("first fetch should download once, got %d", fr.assetHits)
	}
	// Second run with a valid sidecar + matching manifest must not re-download the asset.
	if err := fetch(baseOpts(fr, srv, dest), &out, &errBuf); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if fr.assetHits != 1 {
		t.Fatalf("cache hit should not re-download; asset hits = %d", fr.assetHits)
	}
}

func TestFetchForceRedownloads(t *testing.T) {
	raw := []byte("payload")
	fr, srv := newFakeRelease(t, raw, false)
	defer srv.Close()
	dest := t.TempDir()

	var out, errBuf bytes.Buffer
	if err := fetch(baseOpts(fr, srv, dest), &out, &errBuf); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	o := baseOpts(fr, srv, dest)
	o.force = true
	if err := fetch(o, &out, &errBuf); err != nil {
		t.Fatalf("forced fetch: %v", err)
	}
	if fr.assetHits != 2 {
		t.Fatalf("force should re-download; asset hits = %d", fr.assetHits)
	}
}

func TestFetchChecksumMismatchRejected(t *testing.T) {
	raw := []byte("payload")
	fr, srv := newFakeRelease(t, raw, true) // manifest advertises a wrong checksum
	defer srv.Close()
	dest := t.TempDir()

	var out, errBuf bytes.Buffer
	err := fetch(baseOpts(fr, srv, dest), &out, &errBuf)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "libimbhgo.a")); !os.IsNotExist(statErr) {
		t.Fatalf("archive must not be installed on checksum mismatch")
	}
}

func TestFetchPrintEnv(t *testing.T) {
	raw := []byte("payload")
	fr, srv := newFakeRelease(t, raw, false)
	defer srv.Close()
	dest := t.TempDir()

	o := baseOpts(fr, srv, dest)
	o.printEnv = true
	var out, errBuf bytes.Buffer
	if err := fetch(o, &out, &errBuf); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	stdout := out.String()
	wantL := "-L" + dest
	if !strings.HasPrefix(stdout, "export CGO_LDFLAGS=") || !strings.Contains(stdout, wantL) || !strings.Contains(stdout, "-limbhgo") {
		t.Fatalf("unexpected -print-env stdout: %q", stdout)
	}
	// Only the export line belongs on stdout so `eval "$(...)"` is safe.
	if strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
		t.Fatalf("stdout must be a single export line, got: %q", stdout)
	}
}

// TestFetchPrintEnvIsPOSIXForWindowsTarget is the regression gate for the windows/amd64 CI break:
// -print-env used to switch to cmd.exe's `set VAR=…` whenever the TARGET was Windows, but the
// consumer evaluates the line in git-bash, where `set` assigns positional parameters and leaves
// CGO_LDFLAGS empty. The dialect must follow -shell only.
func TestFetchPrintEnvIsPOSIXForWindowsTarget(t *testing.T) {
	raw := []byte("payload")
	fr, srv := newFakeRelease(t, raw, false)
	defer srv.Close()
	dest := t.TempDir()

	o := baseOpts(fr, srv, dest)
	o.printEnv = true
	o.goos = "windows"
	o.goarch = "amd64"
	o.libc = ""
	// The fake release only serves the linux cell, so let the cached-archive fallback carry the
	// windows target through to emit; what matters here is the shape of the printed line.
	o.baseURL = "http://127.0.0.1:0"
	if err := os.WriteFile(filepath.Join(dest, "libimbhgo.a"), raw, 0o644); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	var out, errBuf bytes.Buffer
	if err := fetch(o, &out, &errBuf); err != nil {
		t.Fatalf("fetch: %v\nstderr:\n%s", err, errBuf.String())
	}
	stdout := strings.TrimSpace(out.String())
	if !strings.HasPrefix(stdout, "export CGO_LDFLAGS=") {
		t.Fatalf("windows target must still print POSIX syntax, got: %q", stdout)
	}
	if strings.HasPrefix(stdout, "set ") {
		t.Fatalf("cmd.exe syntax leaked into the default dialect: %q", stdout)
	}
}

func TestEnvLineDialects(t *testing.T) {
	const value = `-LC:\Users\me\lib -limbhgo`
	for _, tc := range []struct {
		shell string
		want  string
	}{
		{shellSh, `export CGO_LDFLAGS='-LC:\Users\me\lib -limbhgo'`},
		{shellCmd, `set CGO_LDFLAGS=-LC:\Users\me\lib -limbhgo`},
		{shellPowerShell, `$env:CGO_LDFLAGS = '-LC:\Users\me\lib -limbhgo'`},
	} {
		if got := envLine(tc.shell, "CGO_LDFLAGS", value); got != tc.want {
			t.Errorf("envLine(%q) = %q, want %q", tc.shell, got, tc.want)
		}
	}
}

func TestEnvLineQuotesEmbeddedQuote(t *testing.T) {
	if got, want := posixQuote(`a'b`), `'a'\''b'`; got != want {
		t.Errorf("posixQuote = %q, want %q", got, want)
	}
	if got, want := powerShellQuote(`a'b`), `'a''b'`; got != want {
		t.Errorf("powerShellQuote = %q, want %q", got, want)
	}
}

// A cache dir with a space must reach cmd/go as ONE field: its splitter only honours a quote that
// opens a field, so quoting the path alone ("-L'C:\Program Files'") would split into two arguments.
func TestLdflagsForQuotesWholeSearchPath(t *testing.T) {
	if got, want := ldflagsFor("/home/me/lib"), "-L/home/me/lib -limbhgo"; got != want {
		t.Errorf("ldflagsFor = %q, want %q", got, want)
	}
	got := ldflagsFor(`C:\Program Files\imbhgo`)
	want := `'-LC:\Program Files\imbhgo' -limbhgo`
	if got != want {
		t.Errorf("ldflagsFor = %q, want %q", got, want)
	}
}

func TestRunRejectsUnknownShell(t *testing.T) {
	var out, errBuf bytes.Buffer
	err := run([]string{"-shell", "fish", "-print-env"}, &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "unknown -shell") {
		t.Fatalf("expected unknown -shell error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("nothing may reach stdout on a bad flag, got %q", out.String())
	}
}

func TestParseSums(t *testing.T) {
	manifest := "# comment\n" +
		"aaaa  other-file.a.zst\n" +
		"BBBB *libimbhgo-v1-linux-amd64.a.zst\n"
	sum, ok := parseSums(manifest, "libimbhgo-v1-linux-amd64.a.zst")
	if !ok || sum != "bbbb" {
		t.Fatalf("parseSums = %q, %v; want bbbb, true", sum, ok)
	}
	if _, ok := parseSums(manifest, "missing.a.zst"); ok {
		t.Fatal("parseSums should miss unknown asset")
	}
}

func TestAssetNaming(t *testing.T) {
	cases := []struct {
		goos, goarch, libc, want string
	}{
		{"linux", "amd64", release.LibcGlibc, "libimbhgo-v1-linux-amd64.a.zst"},
		{"linux", "arm64", release.LibcMusl, "libimbhgo-v1-linux-arm64-musl.a.zst"},
		{"darwin", "arm64", "", "libimbhgo-v1-darwin-arm64.a.zst"},
		{"windows", "amd64", "", "libimbhgo-v1-windows-amd64.a.zst"},
	}
	for _, c := range cases {
		if got := release.AssetName("v1", c.goos, c.goarch, c.libc); got != c.want {
			t.Errorf("AssetName(%s,%s,%s) = %s; want %s", c.goos, c.goarch, c.libc, got, c.want)
		}
	}
}

func TestDefaultLibc(t *testing.T) {
	musl := release.DefaultLibc("linux", func(string) bool { return true })
	if musl != release.LibcMusl {
		t.Errorf("musl detection = %q; want musl", musl)
	}
	glibc := release.DefaultLibc("linux", func(string) bool { return false })
	if glibc != release.LibcGlibc {
		t.Errorf("glibc detection = %q; want glibc", glibc)
	}
	if none := release.DefaultLibc("darwin", func(string) bool { return true }); none != "" {
		t.Errorf("non-linux libc = %q; want empty", none)
	}
}
