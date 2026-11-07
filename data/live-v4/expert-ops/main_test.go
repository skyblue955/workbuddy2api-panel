package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type fakeRewardClient struct {
	before, after                        []upstream.Task
	resource                             []int64
	listCalls, resourceCalls, claimCalls int
	claimErr                             error
}

func (f *fakeRewardClient) ListTasks(*auth.Auth) ([]upstream.Task, error) {
	f.listCalls++
	if f.listCalls == 1 {
		return f.before, nil
	}
	return f.after, nil
}
func (f *fakeRewardClient) UserResource(*auth.Auth) (int64, int64, error) {
	f.resourceCalls++
	if len(f.resource) == 0 {
		return 0, 0, errors.New("resource gate")
	}
	v := f.resource[0]
	f.resource = f.resource[1:]
	return v, v, nil
}
func (f *fakeRewardClient) ClaimRewardReceipt(*auth.Auth, string) (upstream.ClaimReceipt, error) {
	f.claimCalls++
	return upstream.ClaimReceipt{Credit: 100}, f.claimErr
}

func qualified() upstream.Task {
	return upstream.Task{TaskCode: codes[0], Target: 5, Current: 5, Claimable: true}
}
func TestClaimOneQualifiedAndBoundedReads(t *testing.T) {
	d := t.TempDir()
	f := &fakeRewardClient{before: []upstream.Task{qualified()}, after: []upstream.Task{qualified()}, resource: []int64{30, 20}}
	s, e := claimOne(f, &auth.Auth{}, codes[0], d)
	if e != nil {
		t.Fatal(e)
	}
	if f.listCalls != 2 || f.resourceCalls != 2 || f.claimCalls != 1 || !s.PreValid || !s.PostValid || s.Delta != -10 {
		t.Fatalf("bounded protocol calls=%d/%d/%d summary=%+v", f.listCalls, f.resourceCalls, f.claimCalls, s)
	}
}
func TestClaimOneRejectsUnqualifiedStatesWithoutPOST(t *testing.T) {
	for _, tc := range []struct {
		name string
		task upstream.Task
	}{
		{"notready", upstream.Task{TaskCode: codes[0], Target: 5, Current: 4, Claimable: false}},
		{"locked", upstream.Task{TaskCode: codes[0], Target: 5, Current: 5, Claimable: true, Locked: true}},
		{"duplicate", upstream.Task{TaskCode: codes[0], Target: 5, Current: 5, Claimable: true, Claimed: true}},
		{"buddy", upstream.Task{TaskCode: codes[0], Target: 5, Current: 5, Claimable: true, RewardBuddy: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRewardClient{before: []upstream.Task{tc.task}}
			_, e := claimOne(f, &auth.Auth{}, codes[0], t.TempDir())
			if e == nil {
				t.Fatal("expected qualification error")
			}
			if f.resourceCalls != 0 || f.claimCalls != 0 {
				t.Fatalf("state=%s made mutation resource=%d claim=%d", tc.name, f.resourceCalls, f.claimCalls)
			}
		})
	}
}
func TestClaimOneResourceGateDoesNotRetryOrClaim(t *testing.T) {
	f := &fakeRewardClient{before: []upstream.Task{qualified()}, resource: []int64{}}
	_, e := claimOne(f, &auth.Auth{}, codes[0], t.TempDir())
	if e == nil {
		t.Fatal("expected pre-resource error")
	}
	if f.resourceCalls != 1 || f.claimCalls != 0 {
		t.Fatalf("resource=%d claim=%d", f.resourceCalls, f.claimCalls)
	}
}
func TestClaimOneMarkerCapsDuplicateClaim(t *testing.T) {
	d := t.TempDir()
	f := &fakeRewardClient{before: []upstream.Task{qualified()}, resource: []int64{10, 10}}
	if _, e := claimOne(f, &auth.Auth{}, codes[0], d); e != nil {
		t.Fatal(e)
	}
	f.before = []upstream.Task{qualified()}
	f.resource = []int64{10, 10}
	if _, e := claimOne(f, &auth.Auth{}, codes[0], d); e == nil {
		t.Fatal("duplicate marker should block claim")
	}
	if f.claimCalls != 1 {
		t.Fatalf("claim calls=%d", f.claimCalls)
	}
	if _, e := os.Stat(filepath.Join(d, "claim-"+codes[0]+".started.private")); e != nil {
		t.Fatal(e)
	}
}
