package agent

import (
	"context"
	"encoding/base64"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sourceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeSource(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

type sourceFixture struct{ root, upstream, projects string }

func newSourceFixture(t *testing.T) sourceFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for source synchronization tests")
	}
	f := sourceFixture{root: t.TempDir()}
	f.upstream, f.projects = filepath.Join(f.root, "upstream"), filepath.Join(f.root, "projects")
	if err := os.Mkdir(f.upstream, 0755); err != nil {
		t.Fatal(err)
	}
	sourceGit(t, f.upstream, "init", "--initial-branch=main")
	sourceGit(t, f.upstream, "config", "user.name", "Sync Test")
	sourceGit(t, f.upstream, "config", "user.email", "sync@example.invalid")
	writeSource(t, filepath.Join(f.upstream, "source.txt"), "upstream v1\n")
	writeSource(t, filepath.Join(f.upstream, ".gitignore"), "ignored.txt\n")
	sourceGit(t, f.upstream, "add", ".")
	sourceGit(t, f.upstream, "commit", "-m", "initial")
	// Only this test process's Git children see the rewrite. No request reaches
	// GitHub and all destructive synchronization runs inside t.TempDir().
	gitConfig := filepath.Join(f.root, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	remoteURL := sourceGitRemoteURL(f.upstream)
	sourceGit(t, f.upstream, "config", "--global", "url."+remoteURL+".insteadOf", "https://github.com/test-owner/source.git")
	return f
}

func (f sourceFixture) prepare(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path, err := PrepareIssueProject(ctx, ProjectMeta{Name: "Source", RepoGH: "test-owner/source"}, []string{f.projects}, "private-test-token")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreprocessClonesThenUpdatesAndDiscardsLocalChanges(t *testing.T) {
	f := newSourceFixture(t)
	path := f.prepare(t)
	if path != filepath.Join(f.projects, "source") {
		t.Fatalf("clone path = %s", path)
	}
	if got := sourceGit(t, path, "branch", "--show-current"); got != "main" {
		t.Fatalf("did not clone default branch: %s", got)
	}
	config, err := os.ReadFile(filepath.Join(path, ".git", "config"))
	if err != nil || strings.Contains(string(config), "private-test-token") || strings.Contains(string(config), "AUTHORIZATION") {
		t.Fatal("clone persisted credentials")
	}
	// Upstream advances while this checkout has a local commit, staged edits,
	// unstaged edits, an untracked file and a file ignored by Git.
	sourceGit(t, path, "config", "user.name", "Sync Test")
	sourceGit(t, path, "config", "user.email", "sync@example.invalid")
	writeSource(t, filepath.Join(path, "local-commit.txt"), "local commit")
	sourceGit(t, path, "add", ".")
	sourceGit(t, path, "commit", "-m", "local only")
	writeSource(t, filepath.Join(path, "source.txt"), "staged")
	sourceGit(t, path, "add", "source.txt")
	writeSource(t, filepath.Join(path, "source.txt"), "unstaged")
	writeSource(t, filepath.Join(path, "untracked.txt"), "untracked")
	writeSource(t, filepath.Join(path, "ignored.txt"), "ignored")
	writeSource(t, filepath.Join(f.upstream, "source.txt"), "upstream v2\n")
	sourceGit(t, f.upstream, "add", ".")
	sourceGit(t, f.upstream, "commit", "-m", "remote update")
	if got := f.prepare(t); got != path {
		t.Fatal("update cloned another directory")
	}
	content, err := os.ReadFile(filepath.Join(path, "source.txt"))
	if err != nil || string(content) != "upstream v2\n" {
		t.Fatalf("not refreshed: %q %v", content, err)
	}
	if sourceGit(t, path, "rev-parse", "HEAD") != sourceGit(t, f.upstream, "rev-parse", "HEAD") {
		t.Fatal("local commit survived synchronization")
	}
	if status := sourceGit(t, path, "status", "--porcelain", "--ignored"); status != "" {
		t.Fatalf("local changes survived: %s", status)
	}
	for _, file := range []string{"local-commit.txt", "untracked.txt", "ignored.txt"} {
		if _, err := os.Stat(filepath.Join(path, file)); !os.IsNotExist(err) {
			t.Fatalf("did not remove %s", file)
		}
	}
}

func TestPreprocessUpdatesCurrentTrackingBranchAndDetachedDefault(t *testing.T) {
	f := newSourceFixture(t)
	path := f.prepare(t)
	sourceGit(t, f.upstream, "switch", "-c", "release")
	writeSource(t, filepath.Join(f.upstream, "source.txt"), "release code")
	sourceGit(t, f.upstream, "commit", "-am", "release")
	sourceGit(t, path, "remote", "set-branches", "origin", "*")
	sourceGit(t, path, "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	sourceGit(t, path, "switch", "-c", "local-release", "--track", "origin/release")
	writeSource(t, filepath.Join(f.upstream, "source.txt"), "release update")
	sourceGit(t, f.upstream, "commit", "-am", "release update")
	f.prepare(t)
	if sourceGit(t, path, "branch", "--show-current") != "local-release" || sourceGit(t, path, "show", "HEAD:source.txt") != "release update" {
		t.Fatal("did not follow the current upstream branch")
	}
	sourceGit(t, path, "checkout", "--detach")
	// Upstream HEAD is release, so detached checkouts synchronize that default.
	f.prepare(t)
	if sourceGit(t, path, "show", "HEAD:source.txt") != "release update" {
		t.Fatal("detached checkout did not use remote default")
	}
}

func TestPreprocessFetchFailureKeepsChangesAndCloneFailureCanRetry(t *testing.T) {
	f := newSourceFixture(t)
	path := f.prepare(t)
	writeSource(t, filepath.Join(path, "source.txt"), "keep on network failure")
	sourceGit(t, f.upstream, "config", "--global", "url.file:///missing-private-test-token.git.insteadOf", "https://github.com/test-owner/source.git")
	// Replace the rewrite rather than depending on ordering of identical keys.
	sourceGit(t, f.upstream, "config", "--global", "--remove-section", "url."+sourceGitRemoteURL(f.upstream))
	_, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{f.projects}, "private-test-token")
	if err == nil || !strings.Contains(err.Error(), "拉取更新失败") || strings.Contains(err.Error(), "private-test-token") {
		t.Fatalf("fetch error missing or secret exposed: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(path, "source.txt"))
	if string(content) != "keep on network failure" {
		t.Fatal("failed fetch discarded local changes")
	}
	other := filepath.Join(f.root, "other-projects")
	_, err = PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{other}, "private-test-token")
	if err == nil || !strings.Contains(err.Error(), "clone 失败") {
		t.Fatalf("clone failure not reported: %v", err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatal("failed clone left a partial checkout")
	}
	sourceGit(t, f.upstream, "config", "--global", "--remove-section", "url.file:///missing-private-test-token.git")
	sourceGit(t, f.upstream, "config", "--global", "url."+sourceGitRemoteURL(f.upstream)+".insteadOf", "https://github.com/test-owner/source.git")
	if _, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{other}, ""); err != nil {
		t.Fatalf("retry after failed clone: %v", err)
	}
}

func sourceGitRemoteURL(path string) string {
	if strings.Contains(filepath.ToSlash(path), ":") {
		return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}).String()
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func TestPreprocessRejectsWrongRepositoryAndPlainFolderWithoutDeletingData(t *testing.T) {
	f := newSourceFixture(t)
	path := f.prepare(t)
	writeSource(t, filepath.Join(path, "source.txt"), "keep wrong repo")
	sourceGit(t, path, "remote", "set-url", "origin", "https://github.com/another-owner/source.git")
	_, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{f.projects}, "")
	if err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("wrong repo accepted: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(path, "source.txt"))
	if string(content) != "keep wrong repo" {
		t.Fatal("wrong repo was changed")
	}
	plainRoot := filepath.Join(f.root, "plain")
	plain := filepath.Join(plainRoot, "source")
	if err := os.MkdirAll(plain, 0755); err != nil {
		t.Fatal(err)
	}
	writeSource(t, filepath.Join(plain, "important.txt"), "keep")
	_, err = PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{plainRoot}, "")
	if err == nil || !strings.Contains(err.Error(), "不是独立的 Git 仓库") {
		t.Fatalf("plain folder accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plain, "important.txt")); err != nil {
		t.Fatal("plain directory data lost")
	}
	for _, repo := range []string{"../source", "owner/..", "owner/source/extra", "owner/C:escape"} {
		if _, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: repo}, []string{f.projects}, ""); err == nil {
			t.Fatalf("invalid path accepted: %s", repo)
		}
	}
}

func TestPreprocessTimeoutAndGitOutputLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (projectGit{}).run(ctx, t.TempDir(), "status")
	if err == nil || !strings.Contains(err.Error(), "超时或已取消") {
		t.Fatalf("cancellation not reported: %v", err)
	}
	var output projectGitOutput
	output.Write([]byte(strings.Repeat("x", 16000)))
	output.Write([]byte("last error"))
	if len(output.String()) > 8192 || !strings.HasSuffix(output.String(), "last error") {
		t.Fatal("Git error output not bounded")
	}
}

func TestPreprocessGitCredentialsAreEphemeralAndErrorsAreRedacted(t *testing.T) {
	f := newSourceFixture(t)
	git := projectGit{token: "private-test-token"}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + git.token))
	header, err := git.run(context.Background(), f.upstream, "config", "--get", "http.https://github.com/.extraheader")
	if err != nil || header != "AUTHORIZATION: basic "+encoded {
		t.Fatal("Git child did not receive scoped credentials")
	}
	for _, secret := range []string{git.token, encoded} {
		_, err := git.run(context.Background(), f.upstream, "-c", "core.bare="+secret, "status")
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("credential in Git diagnostics not redacted: %v", err)
		}
	}
	config, _ := os.ReadFile(filepath.Join(f.upstream, ".git", "config"))
	if strings.Contains(string(config), git.token) || strings.Contains(string(config), encoded) {
		t.Fatal("credentials persisted in repository config")
	}
}

func TestPreprocessRejectsMissingRemoteBranchBeforeDiscardingAndIgnoresInheritedGitDir(t *testing.T) {
	f := newSourceFixture(t)
	path := f.prepare(t)
	sourceGit(t, path, "switch", "-c", "local-only")
	writeSource(t, filepath.Join(path, "source.txt"), "keep missing branch")
	_, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{f.projects}, "")
	if err == nil || !strings.Contains(err.Error(), "远端分支不存在") {
		t.Fatalf("missing branch not reported: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(path, "source.txt"))
	if string(content) != "keep missing branch" {
		t.Fatal("missing remote branch destroyed changes")
	}
	sourceGit(t, path, "switch", "main")
	t.Setenv("GIT_DIR", filepath.Join(f.upstream, ".git"))
	t.Setenv("GIT_WORK_TREE", f.upstream)
	if got := f.prepare(t); got != path {
		t.Fatal("inherited Git variables redirected synchronization")
	}
	content, _ = os.ReadFile(filepath.Join(path, "source.txt"))
	if string(content) != "upstream v1\n" {
		t.Fatal("inherited Git variables prevented correct reset")
	}
}

func TestPreprocessRefusesEscapingPathAndLinkedWorktree(t *testing.T) {
	f := newSourceFixture(t)
	if _, err := checkedProjectPath(f.projects, f.upstream); err == nil {
		t.Fatal("outside path accepted")
	}
	root := filepath.Join(f.root, "linked-projects")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source")
	sourceGit(t, f.upstream, "worktree", "add", "--detach", path)
	writeSource(t, filepath.Join(path, "source.txt"), "keep linked checkout")
	_, err := PrepareIssueProject(context.Background(), ProjectMeta{RepoGH: "test-owner/source"}, []string{root}, "")
	if err == nil || !strings.Contains(err.Error(), "不是独立的 Git 仓库") {
		t.Fatalf("linked checkout was not rejected: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(path, "source.txt"))
	if string(content) != "keep linked checkout" {
		t.Fatal("linked checkout was changed")
	}
}
