package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withLicenseDir 把许可证目录指向临时目录，并在测试结束后还原。
func withLicenseDir(t *testing.T) string {
	t.Helper()
	old := licenseDir
	licenseDir = localTempDir(t)
	t.Cleanup(func() { licenseDir = old })
	return licenseDir
}

func TestLicenseLibraryScansDirectory(t *testing.T) {
	dir := withLicenseDir(t)
	files := map[string]string{
		"MIT":            "MIT text",
		"Apache-2.0.txt": "Apache text",
		"lgpl-3.0":       "LGPL text, name contains a dot",
		".hidden":        "should be ignored",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got := licenseLibrary()
	want := []string{"Apache-2.0", "lgpl-3.0", "MIT"}
	if len(got) != len(want) {
		t.Fatalf("license list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("license list = %v, want %v", got, want)
		}
	}
}

func TestLicenseLibraryKeepsDottedLicenseNames(t *testing.T) {
	dir := withLicenseDir(t)
	for _, name := range []string{"LGPL-3.0", "GPL-3.0", "Apache-2.0", "LGPL-2.1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got := licenseLibrary()
	if len(got) != 4 {
		t.Fatalf("dotted license names were dropped: %v", got)
	}
}

func TestLicenseLibraryDeduplicatesTxtVariant(t *testing.T) {
	dir := withLicenseDir(t)
	for _, name := range []string{"MIT", "MIT.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	entries := licenseEntries()
	if len(entries) != 1 || entries[0].File != "MIT" {
		t.Fatalf("entries = %+v, want a single entry backed by MIT", entries)
	}
}

func TestLicenseLibraryMissingDirectory(t *testing.T) {
	old := licenseDir
	licenseDir = filepath.Join(localTempDir(t), "missing")
	t.Cleanup(func() { licenseDir = old })
	if got := licenseLibrary(); len(got) != 0 {
		t.Fatalf("missing directory should yield no licenses, got %v", got)
	}
}

func TestLookupLicenseMatchesNameAndIgnoresEscapes(t *testing.T) {
	dir := withLicenseDir(t)
	if err := os.WriteFile(filepath.Join(dir, "MIT"), []byte("mit text"), 0644); err != nil {
		t.Fatalf("write MIT: %v", err)
	}

	cases := []struct {
		query string
		want  bool
	}{
		{"MIT", true},
		{"mit", true},   // 大小写不敏感兜底
		{" MIT ", true}, // 允许首尾空白
		{"", false},
		{"GPL-3.0", false},
		{"../config/auth.yml", false},
		{filepath.Join("..", "auth.yml"), false},
	}
	for _, c := range cases {
		if _, ok := lookupLicense(c.query); ok != c.want {
			t.Fatalf("lookupLicense(%q) = %v, want %v", c.query, ok, c.want)
		}
	}
}

func TestAttachmentHeaderSanitizesFileName(t *testing.T) {
	plain := attachmentHeader("MIT.txt")
	if plain != `attachment; filename="MIT.txt"` {
		t.Fatalf("plain header = %q", plain)
	}

	// 引号会破坏头部语法，中文需要 RFC 5987 的 filename*
	weird := attachmentHeader(`A"B 中文.txt`)
	if strings.Count(weird, `"`) != 2 {
		t.Fatalf("header should contain exactly two quotes: %q", weird)
	}
	if strings.Contains(weird, `A"B`) {
		t.Fatalf("quote was not sanitized: %q", weird)
	}
	if !strings.Contains(weird, "filename*=UTF-8''") {
		t.Fatalf("non-ASCII name should add filename*: %q", weird)
	}
}

func TestHandleLicenseDownload(t *testing.T) {
	dir := withLicenseDir(t)
	body := "MIT License\n\nPermission is hereby granted..."
	if err := os.WriteFile(filepath.Join(dir, "MIT"), []byte(body), 0644); err != nil {
		t.Fatalf("write MIT: %v", err)
	}

	rec := httptest.NewRecorder()
	handleLicenseDownload(rec, httptest.NewRequest(http.MethodGet, "/api/license/download?name=MIT", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="MIT.txt"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	rec = httptest.NewRecorder()
	handleLicenseDownload(rec, httptest.NewRequest(http.MethodGet, "/api/license/download?name=GPL-3.0", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown license status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	handleLicenseDownload(rec, httptest.NewRequest(http.MethodGet, "/api/license/download", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing name status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	handleLicenseDownload(rec, httptest.NewRequest(http.MethodPost, "/api/license/download?name=MIT", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}
