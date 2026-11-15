package bot

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrBindingCode     = errors.New("bot: 绑定码无效、已使用或已过期")
	ErrAlreadyBound    = errors.New("bot: 此目标已经绑定或已在配置文件中指定")
	ErrBindingNotFound = errors.New("bot: 未找到当前用户的绑定")
)

const BindingCodeTTL = 10 * time.Minute

type Binding struct {
	Target    Target    `json:"target"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
}

// Only code hashes are persisted. Tokens and bindings share the notification
// state transaction, so a code cannot be reused after a crash or failed save.
type bindingCode struct {
	Hash      string    `json:"hash"`
	Owner     string    `json:"owner"`
	Provider  string    `json:"provider"`
	Type      string    `json:"type"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BindingStatus struct {
	Bindings         []BindingView `json:"bindings"`
	PendingExpiresAt *time.Time    `json:"pending_expires_at,omitempty"`
}

type BindingView struct {
	Binding
	Configured bool `json:"configured"`
}

func (s *Service) ProviderEnabled(provider string) bool {
	return s != nil && s.senders[provider] != nil
}

func codeHash(code string) string {
	hash := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(hash[:])
}

func (s *Service) NewBindingCode(owner, provider, kind string) (string, time.Time, error) {
	if owner == "" || strings.TrimSpace(kind) == "" || !s.ProviderEnabled(provider) {
		return "", time.Time{}, fmt.Errorf("bot: 用户或通知平台不可用")
	}
	// The destination ID is unknown until a verified event arrives. Validate
	// the actual target in Bind rather than inventing an ID for the adapter.
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", time.Time{}, err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random[:])
	now := time.Now()
	expires := now.Add(BindingCodeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.Codes
	codes := make([]bindingCode, 0, len(previous)+1)
	for _, item := range previous {
		if item.ExpiresAt.After(now) && !(item.Owner == owner && item.Provider == provider && item.Type == kind) {
			codes = append(codes, item)
		}
	}
	s.state.Codes = append(codes, bindingCode{codeHash(code), owner, provider, kind, expires})
	if err := s.saveLocked(); err != nil {
		s.state.Codes = previous
		return "", time.Time{}, err
	}
	return code, expires, nil
}

func (s *Service) BindingStatus(owner string) BindingStatus {
	result := BindingStatus{Bindings: []BindingView{}}
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.state.Bindings {
		if binding.Owner == owner {
			view := BindingView{Binding: binding}
			for _, target := range s.cfg.Targets {
				if target == binding.Target {
					view.Configured = true
					break
				}
			}
			result.Bindings = append(result.Bindings, view)
		}
	}
	for _, code := range s.state.Codes {
		if code.Owner == owner && code.ExpiresAt.After(time.Now()) {
			expires := code.ExpiresAt
			result.PendingExpiresAt = &expires
		}
	}
	return result
}

// Bind must only be called with a target obtained from an authenticated
// platform event. The one-time code authorizes linking it to its website owner.
func (s *Service) Bind(code string, target Target) error {
	if !s.ProviderEnabled(target.Provider) {
		return ErrBindingCode
	}
	if err := s.senders[target.Provider].Validate(target); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, item := range s.state.Codes {
		if item.Hash == codeHash(code) && item.Provider == target.Provider && item.Type == target.Type && item.ExpiresAt.After(time.Now()) {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrBindingCode
	}
	for _, existing := range s.targetsLocked() {
		if existing == target {
			return ErrAlreadyBound
		}
	}
	previousCodes, previousBindings := s.state.Codes, s.state.Bindings
	s.state.Bindings = append(append([]Binding(nil), previousBindings...), Binding{target, previousCodes[index].Owner, time.Now()})
	s.state.Codes = append(append([]bindingCode(nil), previousCodes[:index]...), previousCodes[index+1:]...)
	if err := s.saveLocked(); err != nil {
		s.state.Codes, s.state.Bindings = previousCodes, previousBindings
		return err
	}
	return nil
}

func (s *Service) Unbind(owner string, target Target) error {
	if s == nil {
		return ErrBindingNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, binding := range s.state.Bindings {
		if binding.Owner == owner && binding.Target == target {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrBindingNotFound
	}
	previousBindings, previousPending := s.state.Bindings, s.state.Pending
	s.state.Bindings = append(append([]Binding(nil), previousBindings[:index]...), previousBindings[index+1:]...)
	// Static configuration can still require delivery to this same destination.
	stillConfigured := false
	for _, existing := range s.cfg.Targets {
		if existing == target {
			stillConfigured = true
		}
	}
	if !stillConfigured {
		s.state.Pending = make([]delivery, 0, len(previousPending))
		for _, item := range previousPending {
			if item.Target != target {
				s.state.Pending = append(s.state.Pending, item)
			}
		}
	}
	if err := s.saveLocked(); err != nil {
		s.state.Bindings, s.state.Pending = previousBindings, previousPending
		return err
	}
	s.notify()
	return nil
}

func (s *Service) targetsLocked() []Target {
	result := append([]Target(nil), s.cfg.Targets...)
	seen := make(map[Target]bool)
	for _, target := range result {
		seen[target] = true
	}
	for _, binding := range s.state.Bindings {
		if !seen[binding.Target] && s.ProviderEnabled(binding.Target.Provider) {
			result = append(result, binding.Target)
			seen[binding.Target] = true
		}
	}
	return result
}
