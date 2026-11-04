package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

type journalRedeemer struct {
	calls int
	err   error
}

func (r *journalRedeemer) Redeem(context.Context, string, workbuddyopen.RedemptionRequest) (*workbuddyopen.RedemptionResponse, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return &workbuddyopen.RedemptionResponse{Status: "success", FlowNo: "flow-fixture", Credits: 3, OpenID: "fixture-open-id"}, nil
}

type concurrentJournalRedeemer struct {
	mu    sync.Mutex
	calls int
}

func (r *concurrentJournalRedeemer) Redeem(context.Context, string, workbuddyopen.RedemptionRequest) (*workbuddyopen.RedemptionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return &workbuddyopen.RedemptionResponse{Status: "success", FlowNo: "flow-fixture", Credits: 3, OpenID: "fixture-open-id"}, nil
}

func writeJournalToken(t *testing.T, path, openID string) {
	writeJournalTokenWithAccess(t, path, openID, "fixture-token")
}

func writeJournalTokenWithAccess(t *testing.T, path, openID, accessToken string) {
	t.Helper()
	doc := tokenDocument{OAuthToken: workbuddyopen.OAuthToken{AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: 3600, Scope: "user.credit.exchange", OpenID: openID}, ObtainedAt: time.Now().UTC()}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemJournalSuccessReplayAndNoSecrets(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	journalDir := filepath.Join(dir, "journal")
	writeJournalToken(t, tokenPath, "fixture-open-id")
	r := &journalRedeemer{}
	args := []string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}
	input := `{"code":"fixture-code","request_id":"request-1"}`
	var out strings.Builder
	if err := run(args, strings.NewReader(input), &out, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || !strings.Contains(out.String(), `"flow_no":"flow-fixture"`) {
		t.Fatalf("calls=%d out=%q", r.calls, out.String())
	}
	entry, err := os.ReadFile(filepath.Join(journalDir, journalHash("request-1")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-code", "fixture-open-id", "fixture-token"} {
		if strings.Contains(string(entry), secret) {
			t.Fatalf("journal contains %q", secret)
		}
	}
	out.Reset()
	if err := run(args, strings.NewReader(input), &out, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || !strings.Contains(out.String(), `"credits":3`) {
		t.Fatalf("replay calls=%d out=%q", r.calls, out.String())
	}
	secondToken := filepath.Join(dir, "rotated-token.json")
	writeJournalTokenWithAccess(t, secondToken, "fixture-open-id", "rotated-token")
	rotatedArgs := []string{"redeem", "-execute", "-token-file", secondToken, "-journal-dir", journalDir}
	if err := run(rotatedArgs, strings.NewReader(input), &out, r); err != nil || r.calls != 1 {
		t.Fatalf("rotated-token replay err=%v calls=%d", err, r.calls)
	}
}

func TestRedeemJournalUncertainAndChangedInputNoNetwork(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	journalDir := filepath.Join(dir, "journal")
	writeJournalToken(t, tokenPath, "fixture-open-id")
	r := &journalRedeemer{err: errors.New("fixture transport")}
	args := []string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}
	if err := run(args, strings.NewReader(`{"code":"fixture-code","request_id":"request-2"}`), &strings.Builder{}, r); err == nil {
		t.Fatal("uncertain request succeeded")
	}
	if err := run(args, strings.NewReader(`{"code":"fixture-code","request_id":"request-2"}`), &strings.Builder{}, r); err == nil || r.calls != 1 {
		t.Fatalf("repeat err=%v calls=%d", err, r.calls)
	}
	if err := run(args, strings.NewReader(`{"code":"changed-code","request_id":"request-2"}`), &strings.Builder{}, r); err == nil || r.calls != 1 {
		t.Fatalf("changed input err=%v calls=%d", err, r.calls)
	}
}

func TestRedeemJournalRequiresOpenIDAndRejectsBadJournal(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	journalDir := filepath.Join(dir, "journal")
	writeJournalToken(t, tokenPath, "")
	r := &journalRedeemer{}
	if err := run([]string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}, strings.NewReader(`{"code":"fixture-code","request_id":"request-3"}`), &strings.Builder{}, r); err == nil || r.calls != 0 {
		t.Fatalf("missing open id err=%v calls=%d", err, r.calls)
	}
	writeJournalToken(t, tokenPath, "fixture-open-id")
	if err := os.Mkdir(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, journalHash("request-3")+".json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}, strings.NewReader(`{"code":"fixture-code","request_id":"request-3"}`), &strings.Builder{}, r); err == nil || r.calls != 0 {
		t.Fatalf("bad journal err=%v calls=%d", err, r.calls)
	}
}

func TestRedeemRequiresExplicitJournalDirAndMatchesResponseSubject(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	writeJournalToken(t, tokenPath, "fixture-open-id")
	args := []string{"redeem", "-execute", "-token-file", tokenPath}
	if err := run(args, strings.NewReader(`{"code":"fixture-code","request_id":"request-5"}`), &strings.Builder{}, &journalRedeemer{}); err == nil {
		t.Fatal("execute accepted missing journal directory")
	}
	journalDir := filepath.Join(dir, "journal")
	// The mock's OpenID is changed to a different subject; the pending record
	// remains and no later invocation can silently replay under another subject.
	if err := run(append(args, "-journal-dir", journalDir), strings.NewReader(`{"code":"fixture-code","request_id":"request-5"}`), &strings.Builder{}, &subjectRedeemer{}); err == nil {
		t.Fatal("subject mismatch accepted")
	}
	if _, err := os.Stat(filepath.Join(journalDir, journalHash("request-5")+".json")); err != nil {
		t.Fatalf("pending record missing: %v", err)
	}
}

type subjectRedeemer struct{}

func (*subjectRedeemer) Redeem(context.Context, string, workbuddyopen.RedemptionRequest) (*workbuddyopen.RedemptionResponse, error) {
	return &workbuddyopen.RedemptionResponse{Status: "success", FlowNo: "flow-fixture", Credits: 3, OpenID: "other-subject"}, nil
}

func TestRedeemJournalConcurrentInvocationsOnePost(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	journalDir := filepath.Join(dir, "journal")
	writeJournalToken(t, tokenPath, "fixture-open-id")
	// Keep the mock response deterministic while allowing both callers to race
	// at the exclusive journal creation boundary.
	adapter := &concurrentJournalRedeemer{}
	args := []string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = run(args, strings.NewReader(`{"code":"fixture-code","request_id":"request-4"}`), &strings.Builder{}, adapter)
		}()
	}
	wg.Wait()
	adapter.mu.Lock()
	calls := adapter.calls
	adapter.mu.Unlock()
	if calls != 1 {
		t.Fatalf("network calls=%d want 1", calls)
	}
}

func TestRedeemJournalRejectsCorruptSucceededReceipt(t *testing.T) {
	for name, receipt := range map[string]map[string]any{
		"missing flow":     {"credits": 1},
		"blank flow":       {"flow_no": " ", "credits": 1},
		"missing credits":  {"flow_no": "flow"},
		"negative credits": {"flow_no": "flow", "credits": -1},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tokenPath := filepath.Join(dir, "token.json")
			journalDir := filepath.Join(dir, "journal")
			writeJournalToken(t, tokenPath, "fixture-open-id")
			if err := os.Mkdir(journalDir, 0700); err != nil {
				t.Fatal(err)
			}
			payloadHash, err := redemptionPayloadHash(workbuddyopen.RedemptionRequest{Code: "fixture-code", RequestID: "request-corrupt"})
			if err != nil {
				t.Fatal(err)
			}
			record := map[string]any{"request_id": "request-corrupt", "payload_hash": payloadHash, "subject_hash": journalHash("fixture-open-id"), "state": "succeeded", "created_at": time.Now().UTC().Format(time.RFC3339Nano), "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
			for k, v := range receipt {
				record[k] = v
			}
			b, _ := json.Marshal(record)
			if err := os.WriteFile(filepath.Join(journalDir, journalHash("request-corrupt")+".json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			r := &journalRedeemer{}
			err = run([]string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}, strings.NewReader(`{"code":"fixture-code","request_id":"request-corrupt"}`), &strings.Builder{}, r)
			if err == nil || r.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, r.calls)
			}
		})
	}
}

func TestRedeemJournalReplaysValidZeroCreditReceipt(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	journalDir := filepath.Join(dir, "journal")
	writeJournalToken(t, tokenPath, "fixture-open-id")
	if err := os.Mkdir(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	in := workbuddyopen.RedemptionRequest{Code: "fixture-code", RequestID: "request-zero"}
	ph, err := redemptionPayloadHash(in)
	if err != nil {
		t.Fatal(err)
	}
	record := redemptionJournalRecord{RequestID: in.RequestID, PayloadHash: ph, SubjectHash: journalHash("fixture-open-id"), State: "succeeded", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), FlowNo: "flow-zero"}
	zero := int64(0)
	record.Credits = &zero
	b, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(journalDir, journalHash(in.RequestID)+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	r := &journalRedeemer{}
	var out strings.Builder
	if err := run([]string{"redeem", "-execute", "-token-file", tokenPath, "-journal-dir", journalDir}, strings.NewReader(`{"code":"fixture-code","request_id":"request-zero"}`), &out, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 || !strings.Contains(out.String(), `"credits":0`) {
		t.Fatalf("calls=%d out=%q", r.calls, out.String())
	}
}

func TestJournalNormalizesTrailingSlashAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token.json")
	writeJournalToken(t, token, "fixture-open-id")
	journal := filepath.Join(dir, "journal")
	r := &journalRedeemer{}
	input := `{"code":"fixture-code","request_id":"request-path"}`
	for _, path := range []string{journal + string(os.PathSeparator), filepath.Join(journal, ".")} {
		if err := run([]string{"redeem", "-execute", "-token-file", token, "-journal-dir", path}, strings.NewReader(input), &strings.Builder{}, r); err != nil {
			t.Fatal(err)
		}
	}
	if r.calls != 1 {
		t.Fatalf("normalizedpath replay posted%d times", r.calls)
	}
	link := filepath.Join(dir, "linked-journal")
	if err := os.Symlink(journal, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := run([]string{"redeem", "-execute", "-token-file", token, "-journal-dir", link + string(os.PathSeparator)}, strings.NewReader(input), &strings.Builder{}, r); err == nil || r.calls != 1 {
		t.Fatal("trailing slash bypassed symlink validation")
	}
}
