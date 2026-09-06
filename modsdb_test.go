package main

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/sqlite"
)

func TestModDifficultyPersistsByPrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.db")
	if err := initModMetaDBAt(path); err != nil {
		t.Fatalf("init mod db: %v", err)
	}
	defer CloseModMetaDB()

	created := ModMeta{ID: "diff-mod", Name: "Diff Mod", Status: "Maintaining"}
	if err := saveModMetaToDB("", created); err != nil {
		t.Fatalf("create mod: %v", err)
	}
	if err := upsertModDifficulty("diff-mod", 4.2); err != nil {
		t.Fatalf("upsert difficulty: %v", err)
	}
	got, err := listModDifficultiesFromDB()
	if err != nil {
		t.Fatalf("list difficulties: %v", err)
	}
	if len(got) != 1 || got["diff-mod"] != 4.2 {
		t.Fatalf("difficulty not persisted: %v", got)
	}

	var pkBefore int64
	if err := modDB.QueryRow("SELECT id FROM mods WHERE mod_id='diff-mod'").Scan(&pkBefore); err != nil {
		t.Fatalf("read pk: %v", err)
	}
	var rowsBefore int
	if err := modDB.QueryRow("SELECT COUNT(*) FROM mod_difficulty").Scan(&rowsBefore); err != nil {
		t.Fatalf("count difficulty rows: %v", err)
	}
	if rowsBefore != 1 {
		t.Fatalf("difficulty rows = %d, want 1", rowsBefore)
	}

	// 改名后主键不变，难度记录应跟随到新 mod_id
	meta, _ := getModMetaFromDB("diff-mod")
	meta.ID = "renamed-diff"
	if err := saveModMetaToDB("diff-mod", meta); err != nil {
		t.Fatalf("rename mod: %v", err)
	}
	got, err = listModDifficultiesFromDB()
	if err != nil {
		t.Fatalf("list difficulties after rename: %v", err)
	}
	if _, stale := got["diff-mod"]; stale {
		t.Fatal("old mod_id still holds a difficulty row after rename")
	}
	if got["renamed-diff"] != 4.2 {
		t.Fatalf("difficulty did not follow rename: %v", got)
	}
	var pkAfter int64
	if err := modDB.QueryRow("SELECT id FROM mods WHERE mod_id='renamed-diff'").Scan(&pkAfter); err != nil {
		t.Fatalf("read pk after rename: %v", err)
	}
	if pkBefore != pkAfter {
		t.Fatalf("surrogate id changed on rename: %d -> %d", pkBefore, pkAfter)
	}

	// 删除模组时外键级联删除难度记录
	if err := deleteModMetaFromDB("renamed-diff"); err != nil {
		t.Fatalf("delete mod: %v", err)
	}
	var rowsAfter int
	if err := modDB.QueryRow("SELECT COUNT(*) FROM mod_difficulty").Scan(&rowsAfter); err != nil {
		t.Fatalf("count difficulty rows after delete: %v", err)
	}
	if rowsAfter != 0 {
		t.Fatalf("difficulty rows after mod delete = %d, want 0", rowsAfter)
	}
}

func TestModLogoPersistsByPrimaryKeyAndFollowsRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.db")
	if err := initModMetaDBAt(path); err != nil {
		t.Fatalf("init mod db: %v", err)
	}
	defer CloseModMetaDB()

	created := ModMeta{ID: "logo-mod", Name: "Logo Mod", Status: "Active"}
	if err := saveModMetaToDB("", created); err != nil {
		t.Fatalf("create mod: %v", err)
	}
	if err := upsertModLogo("logo-mod", []byte("fake-webp-bytes"), "image/webp"); err != nil {
		t.Fatalf("upsert logo: %v", err)
	}
	data, mime, found, err := readModLogo("logo-mod")
	if err != nil || !found {
		t.Fatalf("read logo: found=%v err=%v", found, err)
	}
	if mime != "image/webp" || string(data) != "fake-webp-bytes" {
		t.Fatalf("logo content mismatch: mime=%s data=%q", mime, data)
	}
	var logoCol string
	if err := modDB.QueryRow("SELECT logo FROM mods WHERE mod_id='logo-mod'").Scan(&logoCol); err != nil {
		t.Fatalf("read logo column: %v", err)
	}
	if logoCol != "logo-mod.webp" {
		t.Fatalf("logo column = %q, want logo-mod.webp", logoCol)
	}

	// 改名后主键不变，图片应随新 mod_id 取到
	meta, _ := getModMetaFromDB("logo-mod")
	meta.ID = "logo-renamed"
	if err := saveModMetaToDB("logo-mod", meta); err != nil {
		t.Fatalf("rename mod: %v", err)
	}
	if _, _, found, err := readModLogo("logo-mod"); err != nil || found {
		t.Fatalf("old id still serves logo after rename: found=%v err=%v", found, err)
	}
	data, mime, found, err = readModLogo("logo-renamed")
	if err != nil || !found || mime != "image/webp" || string(data) != "fake-webp-bytes" {
		t.Fatalf("logo lost after rename: found=%v mime=%s data=%q err=%v", found, mime, data, err)
	}

	// 删除模组时外键级联删除图片
	if err := deleteModMetaFromDB("logo-renamed"); err != nil {
		t.Fatalf("delete mod: %v", err)
	}
	var rows int
	if err := modDB.QueryRow("SELECT COUNT(*) FROM mod_logos").Scan(&rows); err != nil {
		t.Fatalf("count logos: %v", err)
	}
	if rows != 0 {
		t.Fatalf("logo rows after delete = %d, want 0", rows)
	}
}

func TestModLogoHandlers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.db")
	if err := initModMetaDBAt(path); err != nil {
		t.Fatalf("init mod db: %v", err)
	}
	defer CloseModMetaDB()

	created := ModMeta{ID: "handler-logo", Name: "Handler Logo", Status: "Active"}
	if err := saveModMetaToDB("", created); err != nil {
		t.Fatalf("create mod: %v", err)
	}
	if err := loadModMetasFromDB(); err != nil {
		t.Fatalf("load metas: %v", err)
	}

	// PUT /api/admin/mods/<id>/logo
	body := "png-bytes-here"
	req := httptest.NewRequest("PUT", "/api/admin/mods/handler-logo/logo", strings.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	rr := httptest.NewRecorder()
	handleAdminMod(rr, req)
	if rr.Code != 200 {
		t.Fatalf("upload logo status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"logo":"handler-logo.png"`) {
		t.Fatalf("upload response missing logo name: %s", rr.Body.String())
	}

	// GET /api/logo/<id>
	req = httptest.NewRequest("GET", "/api/logo/handler-logo", nil)
	rr = httptest.NewRecorder()
	handleModLogo(rr, req)
	if rr.Code != 200 || rr.Body.String() != body {
		t.Fatalf("get logo status=%d body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("get logo content-type = %q", ct)
	}

	// 不存在的模组返回 404
	req = httptest.NewRequest("GET", "/api/logo/not-exist", nil)
	rr = httptest.NewRecorder()
	handleModLogo(rr, req)
	if rr.Code != 404 {
		t.Fatalf("missing logo status = %d", rr.Code)
	}
}
