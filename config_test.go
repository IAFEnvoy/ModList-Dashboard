package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureConfigFilesGeneratesMissingAndKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll(filepath.Join(dir, "config"), 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	// 缺失文件：应从模板生成
	if err := os.WriteFile(filepath.Join(dir, "config", "ai.yml.example"), []byte("template-ai\n"), 0644); err != nil {
		t.Fatalf("write ai template: %v", err)
	}
	// 已有文件：不应被模板覆盖
	if err := os.WriteFile(filepath.Join(dir, "config", "auth.yml"), []byte("keep-me\n"), 0644); err != nil {
		t.Fatalf("write existing auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "auth.yml.example"), []byte("template-auth\n"), 0644); err != nil {
		t.Fatalf("write auth template: %v", err)
	}

	ensureConfigFiles()

	generated, err := os.ReadFile(filepath.Join(dir, "config", "ai.yml"))
	if err != nil {
		t.Fatalf("ai.yml was not generated: %v", err)
	}
	if string(generated) != "template-ai\n" {
		t.Fatalf("ai.yml content = %q, want template", string(generated))
	}
	kept, err := os.ReadFile(filepath.Join(dir, "config", "auth.yml"))
	if err != nil {
		t.Fatalf("auth.yml missing: %v", err)
	}
	if string(kept) != "keep-me\n" {
		t.Fatalf("auth.yml was overwritten: %q", string(kept))
	}
}
