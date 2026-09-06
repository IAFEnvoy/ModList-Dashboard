package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/glebarez/sqlite"
)

const (
	userDBPath        = "data/users.db"
	passwordRounds    = 120000
	minimumPassword   = 8
	authTokenLifetime = 7 * 24 * time.Hour
)

var userDB *sql.DB

type userRecord struct {
	Username     string
	PasswordHash []byte
	Salt         []byte
	Iterations   int
	TokenVersion int
	Enabled      bool
	CreatedAt    string
}

type publicUser struct {
	Username  string `json:"username"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

func InitUserDB() error {
	return initUserDBAt(userDBPath)
}

func initUserDBAt(path string) error {
	CloseUserDB()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create user database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open user database: %w", err)
	}
	db.SetMaxOpenConns(1)
	userDB = db
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		CloseUserDB()
		return fmt.Errorf("enable user database WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		CloseUserDB()
		return fmt.Errorf("configure user database timeout: %w", err)
	}
	if err := createUserSchema(); err != nil {
		CloseUserDB()
		return err
	}
	if err := ensureTokenSecret(); err != nil {
		CloseUserDB()
		return err
	}
	if err := seedDefaultUser(); err != nil {
		CloseUserDB()
		return err
	}
	return nil
}

func CloseUserDB() {
	if userDB != nil {
		_ = userDB.Close()
		userDB = nil
	}
}

func createUserSchema() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS users (
		username       TEXT PRIMARY KEY COLLATE NOCASE,
		password_hash  BLOB NOT NULL,
		salt           BLOB NOT NULL,
		iterations     INTEGER NOT NULL,
		token_version  INTEGER NOT NULL DEFAULT 1,
		enabled        INTEGER NOT NULL DEFAULT 1,
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS auth_meta (
		key   TEXT PRIMARY KEY,
		value BLOB NOT NULL
	);`
	if _, err := userDB.Exec(schema); err != nil {
		return fmt.Errorf("create user database schema: %w", err)
	}
	return nil
}

func ensureTokenSecret() error {
	var secret []byte
	err := userDB.QueryRow("SELECT value FROM auth_meta WHERE key='token_secret'").Scan(&secret)
	if err == nil && len(secret) >= 32 {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read token secret: %w", err)
	}
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate token secret: %w", err)
	}
	if _, err := userDB.Exec("INSERT OR REPLACE INTO auth_meta(key,value) VALUES('token_secret',?)", secret); err != nil {
		return fmt.Errorf("store token secret: %w", err)
	}
	return nil
}

func seedDefaultUser() error {
	var count int
	if err := userDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count != 0 {
		return nil
	}
	username := strings.TrimSpace(authConfig.Admin.Username)
	password := authConfig.Admin.Password
	if username == "" && password == "" {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("default admin username and password must both be configured")
	}
	return createUserWithMinimum(username, password, 1)
}

func createUser(username, password string) error {
	return createUserWithMinimum(username, password, minimumPassword)
}

func createUserWithMinimum(username, password string, minLength int) error {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return errors.New("username must contain 1-64 characters")
	}
	if len(password) < minLength || len(password) > 1024 {
		return fmt.Errorf("password must contain at least %d characters", minLength)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate password salt: %w", err)
	}
	hash := derivePasswordHash(password, salt, passwordRounds)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := userDB.Exec(`INSERT INTO users(username,password_hash,salt,iterations,token_version,enabled,created_at,updated_at)
		VALUES(?,?,?,?,1,1,?,?)`, username, hash, salt, passwordRounds, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("username already exists")
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func authenticateUser(username, password string) bool {
	record, err := getUser(username)
	if err != nil || !record.Enabled {
		dummy := derivePasswordHash(password, []byte("invalid-user-salt"), passwordRounds)
		return subtle.ConstantTimeCompare(dummy, make([]byte, len(dummy))) == 1
	}
	actual := derivePasswordHash(password, record.Salt, record.Iterations)
	return subtle.ConstantTimeCompare(actual, record.PasswordHash) == 1
}

func derivePasswordHash(password string, salt []byte, iterations int) []byte {
	if iterations <= 0 {
		iterations = passwordRounds
	}
	input := make([]byte, 0, len(salt)+len(password))
	input = append(input, salt...)
	input = append(input, password...)
	digest := sha256.Sum256(input)
	passwordBytes := []byte(password)
	for i := 1; i < iterations; i++ {
		h := sha256.New()
		_, _ = h.Write(digest[:])
		_, _ = h.Write(salt)
		_, _ = h.Write(passwordBytes)
		copy(digest[:], h.Sum(nil))
	}
	return append([]byte(nil), digest[:]...)
}

func getUser(username string) (userRecord, error) {
	var u userRecord
	var enabled int
	err := userDB.QueryRow(`SELECT username,password_hash,salt,iterations,token_version,enabled,created_at
		FROM users WHERE username=?`, strings.TrimSpace(username)).Scan(
		&u.Username, &u.PasswordHash, &u.Salt, &u.Iterations, &u.TokenVersion, &enabled, &u.CreatedAt,
	)
	u.Enabled = enabled != 0
	return u, err
}

func listUsers() ([]publicUser, error) {
	rows, err := userDB.Query("SELECT username,enabled,created_at FROM users ORDER BY username COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]publicUser, 0)
	for rows.Next() {
		var u publicUser
		var enabled int
		if err := rows.Scan(&u.Username, &enabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Enabled = enabled != 0
		users = append(users, u)
	}
	return users, rows.Err()
}

func changePassword(username, oldPassword, newPassword string) error {
	if len(newPassword) < minimumPassword || len(newPassword) > 1024 {
		return fmt.Errorf("new password must contain at least %d characters", minimumPassword)
	}
	if !authenticateUser(username, oldPassword) {
		return errors.New("current password is incorrect")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate password salt: %w", err)
	}
	hash := derivePasswordHash(newPassword, salt, passwordRounds)
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := userDB.Exec(`UPDATE users SET password_hash=?,salt=?,iterations=?,token_version=token_version+1,updated_at=?
		WHERE username=? AND enabled=1`, hash, salt, passwordRounds, now, username)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("user not found or disabled")
	}
	return nil
}

func deleteUser(username, currentUsername string) error {
	if strings.EqualFold(strings.TrimSpace(username), strings.TrimSpace(currentUsername)) {
		return errors.New("cannot delete the current account")
	}
	tx, err := userDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=1").Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return errors.New("cannot delete the last enabled account")
	}
	result, err := tx.Exec("DELETE FROM users WHERE username=?", strings.TrimSpace(username))
	if err != nil {
		return err
	}
	deleted, _ := result.RowsAffected()
	if deleted != 1 {
		return errors.New("user not found")
	}
	return tx.Commit()
}

func authEnabled() bool {
	if userDB == nil {
		return false
	}
	var count int
	return userDB.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=1").Scan(&count) == nil && count > 0
}

func signToken(username string) (string, error) {
	record, err := getUser(username)
	if err != nil || !record.Enabled {
		return "", errors.New("user not found or disabled")
	}
	secret, err := tokenSecret()
	if err != nil {
		return "", err
	}
	encodedUser := base64.RawURLEncoding.EncodeToString([]byte(record.Username))
	expires := strconv.FormatInt(time.Now().Add(authTokenLifetime).Unix(), 10)
	version := strconv.Itoa(record.TokenVersion)
	payload := strings.Join([]string{"v1", encodedUser, version, expires}, ".")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func verifyToken(token string) (bool, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "v1" {
		return false, ""
	}
	expires, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false, ""
	}
	secret, err := tokenSecret()
	if err != nil {
		return false, ""
	}
	payload := strings.Join(parts[:4], ".")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[4]), []byte(expected)) != 1 {
		return false, ""
	}
	usernameBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, ""
	}
	version, err := strconv.Atoi(parts[2])
	if err != nil {
		return false, ""
	}
	record, err := getUser(string(usernameBytes))
	if err != nil || !record.Enabled || record.TokenVersion != version {
		return false, ""
	}
	return true, record.Username
}

func tokenSecret() ([]byte, error) {
	var secret []byte
	if userDB == nil {
		return nil, errors.New("user database is not initialized")
	}
	if err := userDB.QueryRow("SELECT value FROM auth_meta WHERE key='token_secret'").Scan(&secret); err != nil {
		return nil, fmt.Errorf("read token secret: %w", err)
	}
	if len(secret) < 32 {
		return nil, errors.New("token secret is invalid")
	}
	return secret, nil
}
