package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"modlist-dashboard/bot"
)

func handleBotBindings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, owner, ok := checkAuth(r)
	if !ok {
		writeAuthError(w, "login required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(struct {
			bot.BindingStatus
			QQEnabled bool `json:"qq_enabled"`
		}{botService.BindingStatus(owner), botService.ProviderEnabled("qq")})
	case http.MethodPost:
		if !botService.ProviderEnabled("qq") {
			writeAuthError(w, "请先配置并启用 QQ Bot，然后重启服务", http.StatusServiceUnavailable)
			return
		}
		code, expires, err := botService.NewBindingCode(owner, "qq", "group")
		if err != nil {
			writeAuthError(w, "无法保存绑定码，请稍后重试", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"code": code, "expires_at": expires})
	case http.MethodDelete:
		var target bot.Target
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&target) != nil || target.Provider == "" || target.ID == "" || target.Type == "" {
			writeAuthError(w, "invalid target", http.StatusBadRequest)
			return
		}
		if err := botService.Unbind(owner, target); err != nil {
			if errors.Is(err, bot.ErrBindingNotFound) {
				writeAuthError(w, "binding not found", http.StatusNotFound)
			} else {
				writeAuthError(w, "无法保存解绑结果，请稍后重试", http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeAuthError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
