package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

const maxJournalRecord = 16 << 10

type redemptionJournalRecord struct {
	RequestID   string `json:"request_id"`
	PayloadHash string `json:"payload_hash"`
	SubjectHash string `json:"subject_hash"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	FlowNo      string `json:"flow_no,omitempty"`
	Credits     *int64 `json:"credits,omitempty"`
}

func journalHash(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }

func redemptionPayloadHash(in workbuddyopen.RedemptionRequest) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return journalHash(string(b)), nil
}

func ensureJournalDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("journal directory is required")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("invalid journal directory")
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func journalPath(dir, requestID string) string {
	return filepath.Join(dir, journalHash(requestID)+".json")
}

func readJournal(path string) (redemptionJournalRecord, error) {
	var rec redemptionJournalRecord
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return rec, errors.New("invalid journal record")
	}
	f, err := os.Open(path)
	if err != nil {
		return rec, err
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) || opened.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		_ = f.Close()
		return rec, errors.New("journal record changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxJournalRecord+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(b) > maxJournalRecord {
		return rec, errors.New("invalid journal record")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return rec, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return rec, errors.New("trailing journal record")
	}
	if rec.RequestID == "" || rec.PayloadHash == "" || rec.SubjectHash == "" || rec.CreatedAt == "" || rec.UpdatedAt == "" || (rec.State != "pending" && rec.State != "succeeded") {
		return rec, errors.New("incomplete journal record")
	}
	if rec.State == "pending" && (rec.FlowNo != "" || rec.Credits != nil) {
		return rec, errors.New("pending journal has a receipt")
	}
	if rec.State == "succeeded" && (strings.TrimSpace(rec.FlowNo) == "" || rec.Credits == nil || *rec.Credits < 0) {
		return rec, errors.New("completed journal receipt is invalid")
	}
	return rec, nil
}

func beginRedemption(dir string, in workbuddyopen.RedemptionRequest, openID string) (redemptionJournalRecord, string, error) {
	if err := ensureJournalDir(dir); err != nil {
		return redemptionJournalRecord{}, "", err
	}
	ph, err := redemptionPayloadHash(in)
	if err != nil {
		return redemptionJournalRecord{}, "", err
	}
	path := journalPath(dir, in.RequestID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := redemptionJournalRecord{RequestID: in.RequestID, PayloadHash: ph, SubjectHash: journalHash(openID), State: "pending", CreatedAt: now, UpdatedAt: now}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		old, readErr := readJournal(path)
		if readErr != nil {
			return rec, "", errors.New("redemption outcome uncertain")
		}
		if old.RequestID != in.RequestID || old.PayloadHash != ph || old.SubjectHash != rec.SubjectHash {
			return rec, "", errors.New("request already used with different input or account")
		}
		if old.State == "succeeded" {
			return old, path, nil
		}
		return old, path, errors.New("redemption outcome uncertain")
	}
	if err := json.NewEncoder(f).Encode(rec); err != nil {
		_ = f.Close()
		return rec, "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return rec, "", err
	}
	if err := f.Close(); err != nil {
		return rec, "", err
	}
	if err := syncDirectory(dir); err != nil {
		return rec, "", err
	}
	return rec, path, nil
}

func completeRedemption(path, dir string, rec redemptionJournalRecord, result *workbuddyopen.RedemptionResponse) error {
	credits := result.Credits
	rec.State, rec.UpdatedAt, rec.FlowNo, rec.Credits = "succeeded", time.Now().UTC().Format(time.RFC3339Nano), result.FlowNo, &credits
	tmp, err := os.CreateTemp(dir, ".redemption-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(rec); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func replayResult(rec redemptionJournalRecord) *workbuddyopen.RedemptionResponse {
	credits := int64(0)
	if rec.Credits != nil {
		credits = *rec.Credits
	}
	return &workbuddyopen.RedemptionResponse{Status: "success", FlowNo: rec.FlowNo, Credits: credits}
}

func validateRedemptionResult(result *workbuddyopen.RedemptionResponse) error {
	if result == nil || result.Status != "success" || strings.TrimSpace(result.FlowNo) == "" || strings.TrimSpace(result.OpenID) == "" || result.Credits < 0 {
		return errors.New("redemption: response is unconfirmed")
	}
	return nil
}
