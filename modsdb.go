package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/sqlite"
)

const modDBPath = "data/mods.db"

var (
	modDB   *sql.DB
	modDBMu sync.Mutex
)

func InitModMetaDB() error {
	return initModMetaDBAt(modDBPath)
}

func initModMetaDBAt(path string) error {
	CloseModMetaDB()
	modDBMu.Lock()
	defer modDBMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create metadata database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open metadata database: %w", err)
	}
	closeOnError := func(err error) error { _ = db.Close(); return err }
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000"); err != nil {
		return closeOnError(fmt.Errorf("configure metadata database: %w", err))
	}
	if _, err := db.Exec(normalizedSchemaSQL); err != nil {
		return closeOnError(fmt.Errorf("create normalized metadata schema: %w", err))
	}
	modDB = db
	return nil
}

const normalizedSchemaSQL = `
CREATE TABLE IF NOT EXISTS mods (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 mod_id TEXT NOT NULL UNIQUE,
 name TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '',
 logo TEXT NOT NULL DEFAULT '',
 package_name TEXT NOT NULL DEFAULT '',
 license TEXT NOT NULL DEFAULT '',
 architecture TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS mod_ids (
 mod_pk INTEGER NOT NULL REFERENCES mods(id) ON DELETE CASCADE,
 platform TEXT NOT NULL,
 value TEXT NOT NULL,
 PRIMARY KEY(mod_pk, platform)
);
CREATE INDEX IF NOT EXISTS idx_mod_ids_platform ON mod_ids(platform);
CREATE TABLE IF NOT EXISTS mod_tags (
 mod_pk INTEGER NOT NULL REFERENCES mods(id) ON DELETE CASCADE,
 tag TEXT NOT NULL,
 PRIMARY KEY(mod_pk, tag)
);
CREATE INDEX IF NOT EXISTS idx_mod_tags_tag ON mod_tags(tag);
CREATE TABLE IF NOT EXISTS mod_versions (
 mod_pk INTEGER NOT NULL REFERENCES mods(id) ON DELETE CASCADE,
 minecraft_version TEXT NOT NULL,
 loader TEXT NOT NULL,
 support_status TEXT NOT NULL,
 PRIMARY KEY(mod_pk, minecraft_version, loader)
);
CREATE INDEX IF NOT EXISTS idx_mod_versions_version ON mod_versions(minecraft_version);
CREATE INDEX IF NOT EXISTS idx_mod_versions_loader ON mod_versions(loader);
CREATE TABLE IF NOT EXISTS mod_coauthors (
 mod_pk INTEGER NOT NULL REFERENCES mods(id) ON DELETE CASCADE,
 username TEXT NOT NULL,
 sort_order INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(mod_pk, username)
);
CREATE INDEX IF NOT EXISTS idx_mod_coauthors_username ON mod_coauthors(username);
CREATE TABLE IF NOT EXISTS mod_difficulty (
 mod_pk INTEGER PRIMARY KEY REFERENCES mods(id) ON DELETE CASCADE,
 score REAL NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS mod_logos (
 mod_pk INTEGER PRIMARY KEY REFERENCES mods(id) ON DELETE CASCADE,
 mime TEXT NOT NULL DEFAULT 'image/webp',
 data BLOB NOT NULL
);

`

func CloseModMetaDB() {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	if modDB != nil {
		_ = modDB.Close()
		modDB = nil
	}
}

func insertModTx(tx *sql.Tx, meta ModMeta, updatedAt string) error {
	if !validModID(meta.ID) || strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("id and name are required")
	}
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	architecture := strings.Join([]string(meta.ModMeta.Architecture), ", ")
	_, err := tx.Exec(`INSERT INTO mods(mod_id,name,description,logo,package_name,license,architecture,status,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(mod_id) DO UPDATE SET name=excluded.name,
 description=excluded.description,logo=excluded.logo,package_name=excluded.package_name,
 license=excluded.license,architecture=excluded.architecture,status=excluded.status,
 updated_at=excluded.updated_at`, meta.ID, meta.Name, meta.Description, meta.Logo,
		meta.ModMeta.Package, meta.ModMeta.License, architecture, meta.Status, updatedAt)
	if err != nil {
		return err
	}
	var modPK int64
	if err := tx.QueryRow("SELECT id FROM mods WHERE mod_id=?", meta.ID).Scan(&modPK); err != nil {
		return err
	}
	for _, table := range []string{"mod_ids", "mod_tags", "mod_versions", "mod_coauthors"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE mod_pk=?", modPK); err != nil {
			return err
		}
	}
	for platform, value := range map[string]string{"github": meta.IDs.GH, "curseforge": meta.IDs.CF, "wiki": meta.IDs.Wiki} {
		if value = strings.TrimSpace(value); value != "" {
			if _, err := tx.Exec("INSERT INTO mod_ids(mod_pk,platform,value) VALUES(?,?,?)", modPK, platform, value); err != nil {
				return err
			}
		}
	}
	for _, tag := range meta.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			if _, err := tx.Exec("INSERT OR IGNORE INTO mod_tags(mod_pk,tag) VALUES(?,?)", modPK, tag); err != nil {
				return err
			}
		}
	}
	for version, loaders := range meta.Versions {
		for loader, status := range loaders {
			if strings.TrimSpace(version) == "" || strings.TrimSpace(loader) == "" {
				continue
			}
			if _, err := tx.Exec("INSERT OR REPLACE INTO mod_versions(mod_pk,minecraft_version,loader,support_status) VALUES(?,?,?,?)", modPK, version, loader, status); err != nil {
				return err
			}
		}
	}
	for i, author := range meta.Coop {
		if author = strings.TrimSpace(author); author != "" {
			if _, err := tx.Exec("INSERT OR REPLACE INTO mod_coauthors(mod_pk,username,sort_order) VALUES(?,?,?)", modPK, author, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func validModID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func readModMetaRow(scanner interface{ Scan(...any) error }) (int64, ModMeta, error) {
	var pk int64
	var meta ModMeta
	var architecture, updatedAt string
	if err := scanner.Scan(&pk, &meta.ID, &meta.Name, &meta.Description, &meta.Logo, &meta.ModMeta.Package,
		&meta.ModMeta.License, &architecture, &meta.Status, &updatedAt); err != nil {
		return 0, ModMeta{}, err
	}
	if architecture != "" {
		meta.ModMeta.Architecture = strings.Split(architecture, ", ")
	}
	// mod_id is the canonical public identifier; the surrogate id stays numeric.
	meta.ModMeta.ModID = meta.ID
	meta.Versions = make(map[string]map[string]string)
	meta.Tags = []string{}
	meta.Coop = []string{}
	return pk, meta, nil
}

func loadChildren(meta *ModMeta, modPK int64) error {
	rows, err := modDB.Query("SELECT platform,value FROM mod_ids WHERE mod_pk=?", modPK)
	if err != nil {
		return err
	}
	for rows.Next() {
		var platform, value string
		if err := rows.Scan(&platform, &value); err != nil {
			rows.Close()
			return err
		}
		switch platform {
		case "github":
			meta.IDs.GH = value
		case "curseforge":
			meta.IDs.CF = value
		case "wiki":
			meta.IDs.Wiki = value
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = modDB.Query("SELECT tag FROM mod_tags WHERE mod_pk=? ORDER BY tag", modPK)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		meta.Tags = append(meta.Tags, tag)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = modDB.Query("SELECT minecraft_version,loader,support_status FROM mod_versions WHERE mod_pk=? ORDER BY minecraft_version,loader", modPK)
	if err != nil {
		return err
	}
	for rows.Next() {
		var version, loader, status string
		if err := rows.Scan(&version, &loader, &status); err != nil {
			rows.Close()
			return err
		}
		if meta.Versions[version] == nil {
			meta.Versions[version] = make(map[string]string)
		}
		meta.Versions[version][loader] = status
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = modDB.Query("SELECT username FROM mod_coauthors WHERE mod_pk=? ORDER BY sort_order,username", modPK)
	if err != nil {
		return err
	}
	for rows.Next() {
		var author string
		if err := rows.Scan(&author); err != nil {
			rows.Close()
			return err
		}
		meta.Coop = append(meta.Coop, author)
	}
	return rows.Close()
}

func listModMetasFromDB() (map[string]ModMeta, error) {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	rows, err := modDB.Query(`SELECT id,mod_id,name,description,logo,package_name,license,architecture,status,updated_at FROM mods ORDER BY mod_id`)
	if err != nil {
		return nil, err
	}
	type storedMeta struct {
		pk   int64
		meta ModMeta
	}
	metas := make([]storedMeta, 0)
	for rows.Next() {
		pk, meta, err := readModMetaRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		metas = append(metas, storedMeta{pk: pk, meta: meta})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make(map[string]ModMeta, len(metas))
	for _, stored := range metas {
		meta := stored.meta
		if err := loadChildren(&meta, stored.pk); err != nil {
			return nil, err
		}
		result[meta.ID] = meta
	}
	return result, nil
}

func getModMetaFromDB(id string) (ModMeta, bool) {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	row := modDB.QueryRow(`SELECT id,mod_id,name,description,logo,package_name,license,architecture,status,updated_at FROM mods WHERE mod_id=?`, id)
	pk, meta, err := readModMetaRow(row)
	if err != nil {
		return ModMeta{}, false
	}
	if err := loadChildren(&meta, pk); err != nil {
		return ModMeta{}, false
	}
	return meta, true
}

// saveModMetaToDB 写入或更新模组元数据。oldID 是更新前的公开 ID，新建时传 ""。
// 当 meta.ID 与 oldID 不同时会在同一事务内把记录改名（自增主键保持不变）。
func saveModMetaToDB(oldID string, meta ModMeta) error {
	if !validModID(meta.ID) || strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("id and name are required; id may contain only letters, numbers, dots, underscores, and hyphens")
	}
	modDBMu.Lock()
	defer modDBMu.Unlock()
	tx, err := modDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if oldID != "" && oldID != meta.ID {
		var collisions int
		if err := tx.QueryRow("SELECT COUNT(*) FROM mods WHERE mod_id=? AND mod_id<>?", meta.ID, oldID).Scan(&collisions); err != nil {
			return err
		}
		if collisions > 0 {
			return fmt.Errorf("a mod with id %q already exists", meta.ID)
		}
		var oldLogo string
		if err := tx.QueryRow("SELECT logo FROM mods WHERE mod_id=?", oldID).Scan(&oldLogo); err != nil {
			return err
		}
		newLogo := oldLogo
		if oldLogo != "" {
			newLogo = meta.ID + filepath.Ext(oldLogo)
		}
		if _, err := tx.Exec("UPDATE mods SET mod_id=?, logo=? WHERE mod_id=?", meta.ID, newLogo, oldID); err != nil {
			return err
		}
	}
	if err := insertModTx(tx, meta, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteModMetaFromDB(id string) error {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	_, err := modDB.Exec("DELETE FROM mods WHERE mod_id=?", id)
	return err
}

// upsertModDifficulty 把 mod_id 的维护难度写入 mod_difficulty（mod_pk 关联自增主键）。
func upsertModDifficulty(modID string, score float64) error {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	var modPK int64
	if err := modDB.QueryRow("SELECT id FROM mods WHERE mod_id=?", modID).Scan(&modPK); err != nil {
		return err
	}
	_, err := modDB.Exec(`INSERT INTO mod_difficulty(mod_pk,score,updated_at) VALUES(?,?,?)
 ON CONFLICT(mod_pk) DO UPDATE SET score=excluded.score, updated_at=excluded.updated_at`,
		modPK, score, time.Now().UTC().Format(time.RFC3339))
	return err
}

// listModDifficultiesFromDB 返回 mod_id -> 难度 的映射（内部缓存用，落盘仍按主键）。
func listModDifficultiesFromDB() (map[string]float64, error) {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	rows, err := modDB.Query(`SELECT m.mod_id, d.score
 FROM mod_difficulty d JOIN mods m ON m.id = d.mod_pk`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	diffs := make(map[string]float64)
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		diffs[id] = score
	}
	return diffs, rows.Err()
}

// ========== 模组 Logo（按主键存库，随 mod_id 改名自动跟随） ==========

var logoMimeExt = map[string]string{
	"image/webp":    "webp",
	"image/png":     "png",
	"image/jpeg":    "jpg",
	"image/gif":     "gif",
	"image/avif":    "avif",
	"image/bmp":     "bmp",
	"image/svg+xml": "svg",
}

func normalizeImageMime(mime string) (string, string) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	ext, ok := logoMimeExt[mime]
	if !ok {
		return "", ""
	}
	return mime, ext
}

// upsertModLogo 把图片二进制写入 mod_logos（按主键关联），并把 logo 列更新为
// "<mod_id>.<扩展名>"，保证改名后仍能通过新 id 取到同一张图。
func upsertModLogo(modID string, data []byte, mime string) error {
	norm, ext := normalizeImageMime(mime)
	if norm == "" {
		return fmt.Errorf("不支持的图片类型: %s", mime)
	}
	modDBMu.Lock()
	defer modDBMu.Unlock()
	var pk int64
	if err := modDB.QueryRow("SELECT id FROM mods WHERE mod_id=?", modID).Scan(&pk); err != nil {
		return err
	}
	tx, err := modDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO mod_logos(mod_pk,mime,data) VALUES(?,?,?)
 ON CONFLICT(mod_pk) DO UPDATE SET mime=excluded.mime, data=excluded.data`, pk, norm, data); err != nil {
		return err
	}
	logoName := modID + "." + ext
	if _, err := tx.Exec("UPDATE mods SET logo=? WHERE id=?", logoName, pk); err != nil {
		return err
	}
	return tx.Commit()
}

// readModLogo 按 mod_id 读取存库图片（供公开接口输出）。
func readModLogo(modID string) (data []byte, mime string, found bool, err error) {
	modDBMu.Lock()
	defer modDBMu.Unlock()
	var pk int64
	if err := modDB.QueryRow("SELECT id FROM mods WHERE mod_id=?", modID).Scan(&pk); err != nil {
		return nil, "", false, nil // 模组不存在按未找到处理
	}
	var logoData []byte
	var logoMime string
	if err := modDB.QueryRow("SELECT data,mime FROM mod_logos WHERE mod_pk=?", pk).Scan(&logoData, &logoMime); err != nil {
		if err == sql.ErrNoRows {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	return logoData, logoMime, true, nil
}

func loadModMetasFromDB() error {
	loaded, err := listModMetasFromDB()
	if err != nil {
		return err
	}
	modMetaMu.Lock()
	modMetas = loaded
	modMetaMu.Unlock()
	log.Printf("[Load] 已从规范化 mods.db 加载 %d 个模组元数据", len(loaded))
	return nil
}

func sortedModMetas() []ModMeta {
	modMetaMu.RLock()
	result := make([]ModMeta, 0, len(modMetas))
	for _, meta := range modMetas {
		result = append(result, meta)
	}
	modMetaMu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func getModMeta(id string) (ModMeta, bool) {
	modMetaMu.RLock()
	meta, ok := modMetas[id]
	modMetaMu.RUnlock()
	return meta, ok
}

func replaceModMeta(meta ModMeta) {
	modMetaMu.Lock()
	modMetas[meta.ID] = meta
	modMetaMu.Unlock()
}

func removeModMeta(id string) {
	modMetaMu.Lock()
	delete(modMetas, id)
	modMetaMu.Unlock()
}
