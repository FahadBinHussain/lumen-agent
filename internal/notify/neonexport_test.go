package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestOrphanExportPush simulates two poll cycles exactly as pushExports does:
// fresh git init -b exports, orphan commit, force-push to a local bare remote.
// After both cycles the remote branch must have exactly 1 commit (flat history)
// and the latest file content.
func TestOrphanExportPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGitTest(t, base, "init", "--bare", remote)

	for cycle, content := range []string{"V1", "V2"} {
		work := filepath.Join(base, "w", string(rune('a'+cycle)))
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, work, "init", "-b", "exports")
		runGitTest(t, work, "config", "user.name", "lumen export")
		runGitTest(t, work, "config", "user.email", "lumen-export@localhost")
		os.MkdirAll(filepath.Join(work, "backups"), 0o755)
		os.WriteFile(filepath.Join(work, "backups", "aged-fire-12399795.sql.gz.enc"), []byte(content), 0o644)
		runGitTest(t, work, "add", ".")
		runGitTest(t, work, "commit", "-m", "export "+content)
		runGitTest(t, work, "push", "--force", remote, "exports")
	}

	count := runGitTest(t, remote, "rev-list", "--count", "exports")
	if count != "1" {
		t.Fatalf("expected flat history (1 commit), got %s commits", count)
	}
	got := runGitTest(t, remote, "show", "exports:backups/aged-fire-12399795.sql.gz.enc")
	if got != "V2" {
		t.Fatalf("expected V2 content, got %q", got)
	}
}