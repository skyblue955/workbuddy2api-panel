package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

type tokenDocument struct {
	workbuddyopen.OAuthToken
	ObtainedAt time.Time `json:"obtained_at"`
}

func readTokenFile(path string) (string, error) {
	initial, err := os.Lstat(path)
	if err != nil || initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return "", errors.New("redeem: token file unavailable")
	}
	if initial.Mode().Perm()&0077 != 0 {
		return "", errors.New("redeem: token file must not be group/world-readable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("redeem: token file unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		return "", errors.New("redeem: token file changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil || len(b) > maxInput {
		return "", errors.New("redeem: token file unreadable")
	}
	var doc tokenDocument
	if err := json.Unmarshal(b, &doc); err != nil || strings.TrimSpace(doc.AccessToken) == "" {
		return "", errors.New("redeem: token file has no access_token")
	}
	if !strings.EqualFold(strings.TrimSpace(doc.TokenType), "bearer") || doc.ExpiresIn <= 0 || doc.ExpiresIn > math.MaxInt64/int64(time.Second) {
		return "", errors.New("redeem: token metadata is invalid")
	}
	now := time.Now()
	if doc.ObtainedAt.IsZero() || doc.ObtainedAt.After(now) || now.After(doc.ObtainedAt.Add(time.Duration(doc.ExpiresIn)*time.Second)) {
		return "", errors.New("redeem: token is expired or obtained_at is invalid")
	}
	if !hasScope(doc.Scope, "user.credit.exchange") {
		return "", errors.New("redeem: token scope is insufficient")
	}
	return doc.AccessToken, nil
}

func hasScope(scope, required string) bool {
	for _, value := range strings.Fields(scope) {
		if value == required {
			return true
		}
	}
	return false
}
