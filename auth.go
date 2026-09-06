package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST required"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !authenticateUser(body.Username, body.Password) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials"})
		return
	}
	record, err := getUser(body.Username)
	if err != nil {
		writeAuthError(w, "login failed", http.StatusInternalServerError)
		return
	}
	token, err := signToken(record.Username)
	if err != nil {
		writeAuthError(w, "login failed", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"token": token, "username": record.Username})
}

func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, username, ok := checkAuth(r)
	json.NewEncoder(w).Encode(map[string]interface{}{"authenticated": ok, "username": username})
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeAuthError(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	_, username, ok := checkAuth(r)
	if !ok {
		writeAuthError(w, "login required", http.StatusUnauthorized)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := changePassword(username, body.CurrentPassword, body.NewPassword); err != nil {
		writeAuthError(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, err := signToken(username)
	if err != nil {
		writeAuthError(w, "password changed, but token refresh failed", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "token": token, "username": username})
}

func handleAuthUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		users, err := listUsers()
		if err != nil {
			writeAuthError(w, "failed to list users", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"users": users})
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAuthError(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := createUser(body.Username, body.Password); err != nil {
			writeAuthError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "created", "username": strings.TrimSpace(body.Username)})
	default:
		writeAuthError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAuthUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodDelete {
		writeAuthError(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	_, currentUsername, ok := checkAuth(r)
	if !ok {
		writeAuthError(w, "login required", http.StatusUnauthorized)
		return
	}
	username, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/auth/users/"))
	if err != nil || strings.TrimSpace(username) == "" || strings.Contains(username, "/") {
		writeAuthError(w, "invalid username", http.StatusBadRequest)
		return
	}
	if err := deleteUser(username, currentUsername); err != nil {
		writeAuthError(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "username": username})
}

func writeAuthError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func checkAuth(r *http.Request) (string, string, bool) {
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		token = token[7:]
	}
	// 也支持 URL 查询参数传递 token（适用于 <a> 下载链接）
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	ok, username := verifyToken(token)
	return token, username, ok
}

// withAuth 包装一个 http.HandlerFunc，要求登录后才能访问
func withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := checkAuth(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "login required"})
			return
		}
		next(w, r)
	}
}
