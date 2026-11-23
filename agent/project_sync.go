package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var githubProjectPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// PrepareIssueProject synchronizes sources before the read-only Agent starts.
// Existing checkouts deliberately discard local edits and commits in favor of
// the current branch's origin upstream. Manual chat project resolution does
// not use this destructive synchronization policy.
func PrepareIssueProject(ctx context.Context, meta ProjectMeta, projectPaths []string, token string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("源码同步超时或已取消：%w", err)
	}
	parts := strings.Split(meta.RepoGH, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("源码同步失败：无效的 GitHub 仓库")
	}
	for _, part := range parts {
		if !githubProjectPart.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(part, ".") || filepath.IsAbs(part) || !filepath.IsLocal(part) {
			return "", fmt.Errorf("源码同步失败：无效的 GitHub 仓库名")
		}
	}
	if len(projectPaths) == 0 {
		return "", fmt.Errorf("源码同步失败：未配置 project_paths")
	}
	git := projectGit{token: token}
	cloneURL := "https://github.com/" + meta.RepoGH + ".git"
	// Search all configured roots before choosing a destination for cloning.
	for _, root := range projectPaths {
		candidate := filepath.Join(root, parts[1])
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("检查项目目录失败：%w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("项目路径不是独立目录：%s", candidate)
		}
		path, err := checkedProjectPath(root, candidate)
		if err != nil {
			return "", err
		}
		if err := git.update(ctx, path, cloneURL, meta.RepoGH); err != nil {
			return "", err
		}
		return path, nil
	}
	var rootErrors []string
	for _, root := range projectPaths {
		if !isDirWritable(root) {
			rootErrors = append(rootErrors, root)
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", fmt.Errorf("解析项目根目录失败：%w", err)
		}
		resolvedRoot, err = filepath.Abs(resolvedRoot)
		if err != nil {
			return "", err
		}
		target := filepath.Join(resolvedRoot, parts[1])
		// Clone into a temporary sibling: failed clones never leave a partial
		// checkout that the next retry would mistake for an existing project.
		staging, err := os.MkdirTemp(resolvedRoot, ".issue-clone-*")
		if err != nil {
			return "", fmt.Errorf("创建克隆临时目录失败：%w", err)
		}
		if _, err := checkedProjectPath(resolvedRoot, staging); err != nil {
			return "", err
		}
		defer os.RemoveAll(staging) // Checked absolute child of the configured root.
		log.Printf("[Issue] 正在克隆源码 %s -> %s", meta.RepoGH, target)
		if _, err := git.run(ctx, "", "clone", "--depth", "1", "--", cloneURL, staging); err != nil {
			return "", fmt.Errorf("项目自动 clone 失败：%w", err)
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return "", fmt.Errorf("克隆目标已存在或不可访问，请重试：%s", target)
		}
		if err := os.Rename(staging, target); err != nil {
			return "", fmt.Errorf("保存克隆项目失败：%w", err)
		}
		log.Printf("[Issue] 源码克隆完成 %s", meta.RepoGH)
		return target, nil
	}
	return "", fmt.Errorf("项目自动 clone 失败：没有可写入的 project_paths（%s）", strings.Join(rootErrors, ", "))
}

// Resolve links before any reset/clean and require a direct child of the
// configured root. In particular a same-named symlink must never redirect a
// destructive Git operation to an unrelated checkout.
func checkedProjectPath(root, candidate string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("解析项目根目录失败：%w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("解析项目目录失败：%w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == "." || !filepath.IsLocal(rel) || filepath.Dir(rel) != "." {
		return "", fmt.Errorf("项目目录超出配置的根目录，已停止同步：%s", candidate)
	}
	return resolved, nil
}

type projectGit struct{ token string }

func (g projectGit) update(ctx context.Context, path, cloneURL, repo string) error {
	// Reject ordinary folders, linked worktrees and submodule roots before
	// running Git: Git otherwise searches parent directories for a repository.
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("源码更新失败：%s 不是独立的 Git 仓库（不支持 .git 文件或链接）", path)
	}
	top, err := g.run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("检查本地仓库失败：%w", err)
	}
	resolvedTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return fmt.Errorf("解析 Git 根目录失败：%w", err)
	}
	if relative, err := filepath.Rel(path, resolvedTop); err != nil || relative != "." {
		return fmt.Errorf("源码更新失败：Git 根目录与项目目录不一致：%s", path)
	}
	origin, err := g.run(ctx, path, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("读取仓库 origin 失败：%w", err)
	}
	if !sameGitHubRepository(origin, repo) {
		return fmt.Errorf("源码更新失败：本地目录的 origin 与 GitHub 仓库 %s 不匹配，未清除本地更改", repo)
	}
	branch, err := g.run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("读取当前分支失败：%w", err)
	}
	ref := "refs/remotes/origin/" + branch
	if branch == "HEAD" {
		remoteHead, err := g.run(ctx, path, "ls-remote", "--symref", cloneURL, "HEAD")
		if err != nil {
			return fmt.Errorf("获取远端默认分支失败：%w", err)
		}
		ref = ""
		for _, line := range strings.Split(remoteHead, "\n") {
			if strings.HasPrefix(line, "ref: refs/heads/") && strings.HasSuffix(line, "\tHEAD") {
				ref = "refs/remotes/origin/" + strings.TrimSuffix(strings.TrimPrefix(line, "ref: refs/heads/"), "\tHEAD")
			}
		}
		if ref == "" {
			return fmt.Errorf("源码更新失败：无法确定远端默认分支")
		}
	} else {
		upstream, err := g.run(ctx, path, "for-each-ref", "--format=%(upstream)", "refs/heads/"+branch)
		if err != nil {
			return fmt.Errorf("读取上游分支失败：%w", err)
		}
		if upstream != "" {
			if !strings.HasPrefix(upstream, "refs/remotes/origin/") {
				return fmt.Errorf("源码更新失败：当前分支的上游不是 origin，未清除本地更改")
			}
			ref = upstream
		}
	}
	log.Printf("[Issue] 正在拉取源码 %s (%s)", repo, ref)
	// Fetch before discarding anything, so network/authentication failures leave
	// the working tree intact. Explicit refspec also updates shallow clones.
	if _, err := g.run(ctx, path, "fetch", "--prune", "--no-tags", "--force", "--", cloneURL, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return fmt.Errorf("项目拉取更新失败（git fetch）：%w", err)
	}
	commit, err := g.run(ctx, path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("远端分支不存在或不可用，未清除本地更改：%w", err)
	}
	if _, err := checkedProjectPath(filepath.Dir(path), path); err != nil {
		return err
	}
	if _, err := g.run(ctx, path, "reset", "--hard", commit); err != nil {
		return fmt.Errorf("重置本地更改失败：%w", err)
	}
	if _, err := g.run(ctx, path, "clean", "-fdx"); err != nil {
		return fmt.Errorf("清理本地未跟踪及忽略文件失败：%w", err)
	}
	log.Printf("[Issue] 源码更新完成 %s，已丢弃本地更改", repo)
	return nil
}

func sameGitHubRepository(raw, repo string) bool {
	if strings.HasPrefix(raw, "git@github.com:") {
		return strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(raw, "git@github.com:"), ".git"), repo)
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "ssh") && strings.EqualFold(u.Host, "github.com") &&
		strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), repo)
}

// Pass credentials through the child environment, never command arguments or
// .git/config. Disable prompting and hooks during unattended synchronization.
func (g projectGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	// Prevent an inherited Git worktree override from redirecting reset/clean.
	var env []string
	for _, entry := range cmd.Env {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if upper == "GIT_DIR" || upper == "GIT_WORK_TREE" || upper == "GIT_INDEX_FILE" || upper == "GIT_COMMON_DIR" || upper == "GIT_CONFIG_COUNT" || upper == "GIT_CONFIG_PARAMETERS" || strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, entry)
	}
	settings := [][2]string{{"core.hooksPath", "/dev/null"}, {"submodule.recurse", "false"}, {"credential.interactive", "false"}}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.token))
	if g.token != "" {
		settings = append(settings, [2]string{"credential.helper", ""}, [2]string{"http.https://github.com/.extraheader", "AUTHORIZATION: basic " + encoded})
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(settings)))
	for i, item := range settings {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, item[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, item[1]))
	}
	cmd.Env = env
	var output projectGitOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		reason := output.String()
		if ctx.Err() != nil {
			reason = "源码同步超时或已取消：" + ctx.Err().Error()
		}
		for _, secret := range []string{g.token, encoded, url.QueryEscape(g.token)} {
			if secret != "" {
				reason = strings.ReplaceAll(reason, secret, "[REDACTED]")
			}
		}
		return "", fmt.Errorf("git %s 失败：%v；%s", args[0], err, strings.TrimSpace(reason))
	}
	return strings.TrimSpace(output.String()), nil
}

// Keep error diagnostics bounded even for verbose clone/fetch progress.
type projectGitOutput struct{ buffer bytes.Buffer }

func (b *projectGitOutput) String() string { return b.buffer.String() }

func (b *projectGitOutput) Write(p []byte) (int, error) {
	const limit = 8192
	n := len(p)
	if len(p) >= limit {
		b.buffer.Reset()
		p = p[len(p)-limit:]
	}
	if extra := b.buffer.Len() + len(p) - limit; extra > 0 {
		b.buffer.Next(extra)
	}
	b.buffer.Write(p)
	return n, nil
}
