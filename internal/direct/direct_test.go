package direct_test

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func roots(t *testing.T, host string) *direct.Set {
	t.Helper()
	set, err := direct.New([]direct.Root{{ImmichPath: "/immich/upload", HostPath: host}})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestEligibleOriginalOpensWithoutChangingTheMount(t *testing.T) {
	host := t.TempDir()
	path := filepath.Join(host, "library", "ab", "cd", "photo.jpg")
	write(t, path, "original bytes")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	set := roots(t, host)
	f, outcome := set.Open("/immich/upload/library/ab/cd/photo.jpg")
	if outcome != direct.Opened || f == nil {
		t.Fatalf("eligible original was not opened: %q", outcome)
	}
	b, err := io.ReadAll(f)
	if string(b) != "original bytes" || err != nil {
		t.Fatalf("read %q, %v", b, err)
	}
	if !f.Stable() {
		t.Fatal("an unchanged file was reported unstable")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime() != info.ModTime() || after.Size() != info.Size() || after.Mode() != info.Mode() {
		t.Fatalf("direct read changed the source: before=%v after=%v", info, after)
	}
	if configured, available := set.Check(); configured != 1 || available != 1 {
		t.Fatalf("root check: configured=%d available=%d", configured, available)
	}
}

func TestDisabledAndIneligiblePathsFallBackToTheAPI(t *testing.T) {
	var disabled *direct.Set
	if f, outcome := disabled.Open("/immich/upload/a.jpg"); f != nil || outcome != direct.NotConfigured {
		t.Fatalf("disabled set resolved a path: %v %q", f, outcome)
	}
	host := t.TempDir()
	write(t, filepath.Join(host, "present.jpg"), "ok")
	set := roots(t, host)
	for _, tc := range []struct {
		name string
		path string
		want direct.Outcome
	}{
		{"no reported path", "", direct.OutsideRoot},
		{"outside an approved root", "/immich/other/present.jpg", direct.OutsideRoot},
		{"absent file under an approved root", "/immich/upload/gone.jpg", direct.Missing},
		{"parent directory of the root", "/immich", direct.OutsideRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if f, outcome := set.Open(tc.path); f != nil || outcome != tc.want {
				t.Fatalf("got %v %q, want %q", f, outcome, tc.want)
			}
		})
	}
	gone := t.TempDir()
	absent := roots(t, gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if f, outcome := absent.Open("/immich/upload/present.jpg"); f != nil || outcome != direct.RootUnavailable {
		t.Fatalf("absent mount: %v %q", f, outcome)
	}
	if configured, available := absent.Check(); configured != 1 || available != 0 {
		t.Fatalf("absent mount check: %d %d", configured, available)
	}
}

func TestUnreadableFileFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission checks do not apply")
	}
	host := t.TempDir()
	path := filepath.Join(host, "secret.jpg")
	write(t, path, "ok")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })
	if f, outcome := roots(t, host).Open("/immich/upload/secret.jpg"); f != nil || outcome != direct.Unreadable {
		t.Fatalf("unreadable file: %v %q", f, outcome)
	}
}

func TestTraversalAndSymlinkEscapesFailClosed(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "root")
	write(t, filepath.Join(host, "inside.jpg"), "inside")
	write(t, filepath.Join(base, "outside.jpg"), "outside")
	if err := os.Symlink(filepath.Join(base, "outside.jpg"), filepath.Join(host, "escape.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(host, "absolute.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.jpg", filepath.Join(host, "alias.jpg")); err != nil {
		t.Fatal(err)
	}
	set := roots(t, host)
	for _, tc := range []struct {
		name string
		path string
		want direct.Outcome
	}{
		{"traversal", "/immich/upload/../outside.jpg", direct.PathInvalid},
		{"relative", "immich/upload/inside.jpg", direct.PathInvalid},
		{"unclean", "/immich/upload//inside.jpg", direct.PathInvalid},
		{"symlink escape", "/immich/upload/escape.jpg", direct.PathEscape},
		{"absolute symlink", "/immich/upload/absolute.jpg", direct.PathEscape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if f, outcome := set.Open(tc.path); f != nil || outcome != tc.want || !outcome.FailClosed() {
				t.Fatalf("got %v %q, want fail-closed %q", f, outcome, tc.want)
			}
		})
	}
	f, outcome := set.Open("/immich/upload/alias.jpg")
	if outcome != direct.Opened || f == nil {
		t.Fatalf("contained symlink was rejected: %q", outcome)
	}
	b, err := io.ReadAll(f)
	f.Close()
	if string(b) != "inside" || err != nil {
		t.Fatalf("contained symlink read %q, %v", b, err)
	}
}

func TestDevicesSocketsAndDirectoriesAreRejected(t *testing.T) {
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(host, "folder.jpg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(host, "pipe.jpg"), 0644); err != nil {
		t.Skipf("fifo unavailable: %v", err)
	}
	socket, err := net.Listen("unix", filepath.Join(host, "socket.jpg"))
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	defer socket.Close()
	set := roots(t, host)
	for _, path := range []string{"/immich/upload/folder.jpg", "/immich/upload/pipe.jpg", "/immich/upload/socket.jpg"} {
		if f, outcome := set.Open(path); f != nil || outcome != direct.PathNotRegular || !outcome.FailClosed() {
			t.Fatalf("%s: %v %q", path, f, outcome)
		}
	}
}

func TestMutationDuringTheReadIsNotStable(t *testing.T) {
	host := t.TempDir()
	path := filepath.Join(host, "photo.jpg")
	write(t, path, "first")
	f, outcome := roots(t, host).Open("/immich/upload/photo.jpg")
	if outcome != direct.Opened {
		t.Fatalf("open: %q", outcome)
	}
	defer f.Close()
	if err := os.WriteFile(path, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	if f.Stable() {
		t.Fatal("a file rewritten during the read was reported stable")
	}
}

func TestLongestApprovedRootWins(t *testing.T) {
	outer, inner := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outer, "photos", "a.jpg"), "outer")
	write(t, filepath.Join(inner, "a.jpg"), "inner")
	set, err := direct.New([]direct.Root{
		{ImmichPath: "/immich", HostPath: outer},
		{ImmichPath: "/immich/photos", HostPath: inner},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, outcome := set.Open("/immich/photos/a.jpg")
	if outcome != direct.Opened {
		t.Fatalf("open: %q", outcome)
	}
	b, err := io.ReadAll(f)
	f.Close()
	if string(b) != "inner" || err != nil {
		t.Fatalf("read %q, %v", b, err)
	}
}

func TestInvalidMappingsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roots []direct.Root
	}{
		{"relative immich path", []direct.Root{{ImmichPath: "photos", HostPath: t.TempDir()}}},
		{"relative host path", []direct.Root{{ImmichPath: "/immich", HostPath: "photos"}}},
		{"unclean immich path", []direct.Root{{ImmichPath: "/immich/", HostPath: t.TempDir()}}},
		{"empty mapping", []direct.Root{{}}},
		{"duplicate immich path", []direct.Root{
			{ImmichPath: "/immich", HostPath: t.TempDir()},
			{ImmichPath: "/immich", HostPath: t.TempDir()},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if set, err := direct.New(tc.roots); err == nil || set != nil {
				t.Fatalf("accepted an unsafe mapping: %v", err)
			}
		})
	}
}
