package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestResolvePathWithinRejectsTraversal(t *testing.T) {
	root := localTempDir(t)
	inside, err := resolvePathWithin(root, "folder/file.txt")
	if err != nil {
		t.Fatalf("resolve inside path: %v", err)
	}
	if !pathWithinRoot(root, inside) {
		t.Fatalf("inside path escaped root: %s", inside)
	}
	if _, err := resolvePathWithin(root, filepath.Join("..", "drive-backup", "file.txt")); err == nil {
		t.Fatal("sibling traversal was accepted")
	}
	if _, err := resolvePathWithin(root, filepath.Join(root, "absolute.txt")); err == nil {
		t.Fatal("absolute path was accepted")
	}
}

func TestResolvePathWithinRejectsEscapingSymlink(t *testing.T) {
	root := localTempDir(t)
	outside := localTempDir(t)
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := resolvePathWithin(root, filepath.Join("outside-link", "file.txt")); err == nil {
		t.Fatal("path through escaping symlink was accepted")
	}
}

func TestConcurrentTodoAppendsDoNotLoseItems(t *testing.T) {
	oldTodoFile := todoFile
	todoFile = filepath.Join(localTempDir(t), "todos.json")
	defer func() { todoFile = oldTodoFile }()
	if err := os.WriteFile(todoFile, []byte(`{"items":[]}`), 0644); err != nil {
		t.Fatalf("seed todo file: %v", err)
	}

	const count = 40
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			errs <- appendTodoItem(TodoItem{ID: string(rune('a' + id)), Type: "todo", Title: "item"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append todo: %v", err)
		}
	}
	if got := len(loadTodos().Items); got != count {
		t.Fatalf("todo count = %d, want %d", got, count)
	}
}

func localTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".modlist-test-")
	if err != nil {
		t.Fatalf("create local temp dir: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve local temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	return abs
}
