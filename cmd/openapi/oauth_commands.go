package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

const (
	oauthScope   = "user.credit.exchange"
	maxOAuthFile = 64 << 10
	oauthMaxAge  = 15 * time.Minute
)

type oauthSession struct {
	ClientID    string    `json:"client_id"`
	RedirectURI string    `json:"redirect_uri"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
}

type oauthCallback struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

func runAuthorize(args []string, out io.Writer, client *workbuddyopen.Client) error {
	fs := newOAuthFlagSet("authorize")
	clientID := fs.String("client-id", "", "official OAuth client id")
	redirectURI := fs.String("redirect-uri", "", "registered redirect URI")
	statePath := fs.String("state-file", "", "state session file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("authorize: invalid arguments")
	}
	if strings.TrimSpace(*clientID) == "" || strings.TrimSpace(*redirectURI) == "" || strings.TrimSpace(*statePath) == "" {
		return errors.New("authorize: -client-id, -redirect-uri and -state-file are required")
	}
	state, err := workbuddyopen.NewState()
	if err != nil {
		return errors.New("authorize: could not create state")
	}
	url, err := client.AuthorizeURL(*clientID, *redirectURI, oauthScope, state)
	if err != nil {
		return errors.New("authorize: invalid OAuth parameters")
	}
	session := oauthSession{ClientID: *clientID, RedirectURI: *redirectURI, State: state, CreatedAt: time.Now().UTC()}
	if err := createJSON0600(*statePath, session); err != nil {
		return errors.New("authorize: state file unavailable")
	}
	_, err = io.WriteString(out, url+"\n")
	return err
}

func runExchange(args []string, stdin io.Reader, out io.Writer, client *workbuddyopen.Client) error {
	fs := newOAuthFlagSet("exchange")
	statePath := fs.String("state-file", "", "state session file")
	tokenPath := fs.String("token-file", "", "token output file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("exchange: invalid arguments")
	}
	if strings.TrimSpace(*statePath) == "" || strings.TrimSpace(*tokenPath) == "" {
		return errors.New("exchange: -state-file and -token-file are required")
	}
	callback, err := decodeOAuthCallback(stdin)
	if err != nil {
		return err
	}
	session, err := readOAuthSession(*statePath)
	if err != nil {
		return errors.New("exchange: state file unavailable")
	}
	now := time.Now().UTC()
	if session.CreatedAt.After(now) || now.Sub(session.CreatedAt) > oauthMaxAge {
		return errors.New("exchange: state expired")
	}
	if !workbuddyopen.VerifyState(session.State, callback.State) {
		return errors.New("exchange: state mismatch")
	}
	secret := os.Getenv("WORKBUDDY_OPEN_CLIENT_SECRET")
	if strings.TrimSpace(secret) == "" {
		return errors.New("exchange: client secret is required")
	}
	// Refuse an existing destination before consuming the one-use code.
	if _, err := os.Lstat(*tokenPath); !os.IsNotExist(err) {
		return errors.New("exchange: token destination already exists or is unavailable")
	}
	// Claim this session once before submitting the one-use authorization code.
	// An uncertain network/save failure requires a new authorization flow, never
	// an automatic replay with a different destination file.
	if err := createJSON0600(*statePath+".used", map[string]bool{"submitted": true}); err != nil {
		return errors.New("exchange: session already submitted; start a new authorization flow")
	}
	token, err := client.ExchangeCode(context.Background(), session.ClientID, secret, callback.Code, session.RedirectURI)
	if err != nil {
		return errors.New("exchange: token exchange failed")
	}
	if err := createJSON0600(*tokenPath, tokenDocument{OAuthToken: *token, ObtainedAt: time.Now().UTC()}); err != nil {
		return errors.New("exchange: token file unavailable")
	}
	if err := os.Remove(*statePath); err != nil {
		_, err = io.WriteString(out, "token saved; state cleanup pending; do not exchange again\n")
		return err
	}
	_ = os.Remove(*statePath + ".used") // state no longer exists; marker cleanup is best effort.
	_, err = io.WriteString(out, "token saved\n")
	return err
}

func newOAuthFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func decodeOAuthCallback(r io.Reader) (oauthCallback, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxOAuthFile+1))
	if err != nil || len(b) > maxOAuthFile {
		return oauthCallback{}, errors.New("exchange: invalid callback input")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var callback oauthCallback
	if err := dec.Decode(&callback); err != nil {
		return callback, errors.New("exchange: callback must be one JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return callback, errors.New("exchange: trailing JSON is not allowed")
	}
	if strings.TrimSpace(callback.Code) == "" || strings.TrimSpace(callback.State) == "" {
		return callback, errors.New("exchange: code and state are required")
	}
	return callback, nil
}

func createJSON0600(path string, value any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readOAuthSession(path string) (oauthSession, error) {
	var session oauthSession
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return session, errors.New("invalid session file")
	}
	f, err := os.Open(path)
	if err != nil {
		return session, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return session, errors.New("session changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxOAuthFile+1))
	if err != nil || len(b) > maxOAuthFile {
		return session, errors.New("session unreadable")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&session); err != nil {
		return session, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return session, errors.New("trailing session JSON")
	}
	if session.ClientID == "" || session.RedirectURI == "" || session.State == "" || session.CreatedAt.IsZero() {
		return session, errors.New("incomplete session")
	}
	return session, nil
}
