//go:build sable_extern_lib

package imbhgo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed open must report the reason imbh gave, not just "failed". The imbhgo_open* entry points
// are direct C calls whose only return is a u64 handle, so the cause travels out-of-band under a
// caller-allocated err_id; this is the gate on that path staying wired. Before it existed, every
// open failure — wrong permissions, a writer lock held elsewhere, an unsupported platform — reduced
// to one indistinguishable message, which is exactly what made the first windows/amd64 CI failure
// undiagnosable.
func TestOpenErrorReportsCause(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	defer db.Close()

	// imbh takes an exclusive advisory lock on <dir>/writer.lock for a ReadWrite open, so a second
	// one fails whether it comes from this process or another (imbh's own cross_process test relies
	// on the same equivalence).
	second, err := Open(dir)
	if err == nil {
		second.Close()
		t.Fatal("second Open of the same path succeeded; expected the writer lock to reject it")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error does not name the path: %v", err)
	}
	// The whole point: something beyond the generic suffix made it back across the FFI boundary.
	if strings.HasSuffix(err.Error(), " failed") {
		t.Errorf("error carries no cause from imbh, only the generic form: %v", err)
	}
}

// The options open reports its cause too, on a path imbh cannot create a database at.
func TestOpenWithErrorReportsCause(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "regular.file")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	db, err := OpenWith(DbOptions{Path: filepath.Join(regular, "db")})
	if err == nil {
		db.Close()
		t.Fatal("OpenWith succeeded under a regular file; expected a not-a-directory failure")
	}
	if strings.HasSuffix(err.Error(), " failed") {
		t.Errorf("error carries no cause from imbh, only the generic form: %v", err)
	}
}
