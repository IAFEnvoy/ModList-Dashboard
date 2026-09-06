package main

import (
	"path/filepath"
	"testing"
)

func TestUserDBSeedsLegacyAdminAndSupportsMultipleAccounts(t *testing.T) {
	oldConfig := authConfig
	defer func() { authConfig = oldConfig }()
	authConfig.Admin.Username = "legacy-admin"
	authConfig.Admin.Password = "legacy-password"

	path := filepath.Join(t.TempDir(), "users.db")
	if err := initUserDBAt(path); err != nil {
		t.Fatalf("init user db: %v", err)
	}
	defer CloseUserDB()

	if !authenticateUser("legacy-admin", "legacy-password") {
		t.Fatal("legacy admin was not seeded")
	}
	if authenticateUser("legacy-admin", "wrong-password") {
		t.Fatal("wrong password was accepted")
	}
	if err := createUser("second-user", "second-password"); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if !authenticateUser("second-user", "second-password") {
		t.Fatal("second user cannot log in")
	}
	users, err := listUsers()
	if err != nil || len(users) != 2 {
		t.Fatalf("list users = %#v, err=%v", users, err)
	}
}

func TestPasswordChangeInvalidatesOldToken(t *testing.T) {
	oldConfig := authConfig
	defer func() { authConfig = oldConfig }()
	authConfig.Admin.Username = "change-user"
	authConfig.Admin.Password = "initial-password"

	if err := initUserDBAt(filepath.Join(t.TempDir(), "users.db")); err != nil {
		t.Fatalf("init user db: %v", err)
	}
	defer CloseUserDB()

	oldToken, err := signToken("change-user")
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	if err := changePassword("change-user", "initial-password", "replacement-password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if ok, _ := verifyToken(oldToken); ok {
		t.Fatal("old token remained valid after password change")
	}
	newToken, err := signToken("change-user")
	if err != nil {
		t.Fatalf("sign replacement token: %v", err)
	}
	if ok, username := verifyToken(newToken); !ok || username != "change-user" {
		t.Fatalf("replacement token invalid: ok=%v username=%q", ok, username)
	}
}

func TestLegacyHardcodedTokenIsRejected(t *testing.T) {
	oldConfig := authConfig
	defer func() { authConfig = oldConfig }()
	authConfig.Admin.Username = "legacy-admin"
	authConfig.Admin.Password = "legacy-password"

	if err := initUserDBAt(filepath.Join(t.TempDir(), "users.db")); err != nil {
		t.Fatalf("init user db: %v", err)
	}
	defer CloseUserDB()

	if ok, _ := verifyToken("legacy-admin:4102444800:modlist-secret-key-v1"); ok {
		t.Fatal("old hardcoded token format was accepted")
	}
}
