package agent

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectMeta 项目元信息（用于解析项目路径）
type ProjectMeta struct {
	Name   string // 显示名称，如 "MineDash"
	RepoGH string // GitHub 仓库，如 "IAFEnvoy/MineDash"
}

// ResolveProject 在 project_paths 中查找项目目录，找不到就 git clone
// 返回项目在本地磁盘的绝对路径
func ResolveProject(meta ProjectMeta, projectPaths []string) (string, error) {
	if meta.Name == "" {
		return "", fmt.Errorf("项目名称为空")
	}

	// 仓库名：从 GitHub 路径提取 repo 部分
	repoName := meta.Name
	if meta.RepoGH != "" {
		parts := strings.Split(meta.RepoGH, "/")
		if len(parts) == 2 {
			repoName = parts[1]
		}
	}

	// Step 1: 在配置的路径中搜索
	for _, root := range projectPaths {
		candidate := filepath.Join(root, repoName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			abs, _ := filepath.Abs(candidate)
			log.Printf("[Path] 项目 %s 已存在于 %s", meta.Name, abs)
			return abs, nil
		}
	}

	// Step 2: 找不到，git clone 到第一个可写入路径
	if meta.RepoGH == "" {
		return "", fmt.Errorf("项目 %s 未配置 GitHub 仓库，无法自动克隆", meta.Name)
	}

	targetRoot := ""
	for _, root := range projectPaths {
		abs, _ := filepath.Abs(root)
		if isDirWritable(abs) {
			targetRoot = abs
			break
		}
	}
	if targetRoot == "" {
		return "", fmt.Errorf("没有可写入的项目目录（已检查 %v）", projectPaths)
	}

	targetPath := filepath.Join(targetRoot, repoName)
	cloneURL := fmt.Sprintf("https://github.com/%s.git", meta.RepoGH)

	log.Printf("[Clone] git clone %s -> %s", cloneURL, targetPath)
	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, targetPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %s 失败: %v", cloneURL, err)
	}

	log.Printf("[√] 项目 %s 已克隆到 %s", meta.Name, targetPath)
	return targetPath, nil
}

// isDirWritable 检查目录是否可写入
func isDirWritable(dir string) bool {
	// 确保目录存在
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false
	}
	// 尝试写临时文件
	f, err := os.CreateTemp(dir, ".writetest_*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
