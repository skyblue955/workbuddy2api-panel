package upstream

import (
	"encoding/json"
	"testing"
)

func TestParseClaimRewardReceiptIntegrity(t *testing.T) {
	valid := []struct {
		name           string
		body           string
		credit, energy int64
		claimed, known bool
	}{
		{"credit", `{"credit":3}`, 3, 0, false, false},
		{"energy", `{"energy":4}`, 0, 4, false, false},
		{"both", `{"credit":3,"energy":4,"extension":{"x":1}}`, 3, 4, false, false},
		{"already", `{"already_claimed":true}`, 0, 0, true, true},
		{"zero-false", `{"credit":0,"energy":0,"already_claimed":false}`, 0, 0, false, true},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseClaimRewardReceipt(json.RawMessage(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if r.Credit != tc.credit || r.Energy != tc.energy || r.AlreadyClaimed != tc.claimed || r.AlreadyClaimedKnown != tc.known {
				t.Fatalf("receipt=%+v", r)
			}
		})
	}
	invalid := []string{
		`null`, `[]`, `{}`, `{"already_claimed":false}`,
		`{"credit":-1}`, `{"energy":-1}`, `{"credit":1.2}`, `{"credit":1e2}`,
		`{"credit":null}`, `{"already_claimed":null}`, `{"already_claimed":true,"credit":1}`,
		`{"credit":1,"credit":2}`, `{"credit":1} trailing`, `{"credit":"3"}`,
		`{"credit":1,"already_claimed":null}`, `{"already_claimed":false,"extension":1}`,
		`{"extension":true}`, `{"credit":9223372036854775808}`,
	}
	for _, body := range invalid {
		t.Run(body, func(t *testing.T) {
			if _, err := parseClaimRewardReceipt(json.RawMessage(body)); err == nil {
				t.Fatalf("accepted invalid receipt %s", body)
			}
		})
	}
}

func TestGrowthProgressAndInactiveEligibility(t *testing.T) {
	data := json.RawMessage(`{"tasks":[
		{"task_code":"locked","locked":true,"target":1,"current":1},
		{"task_code":"ended","status":"EnDeD","target":1,"current":1},
		{"task_code":"zero","target":9,"current":9,"progress":{"current":0,"target":0}},
		{"task_code":"partial","target":9,"current":9,"progress":{"current":1,"target":2}},
		{"task_code":"invalid","target":9,"current":9,"progress":{"current":"bad","target":9}},
		{"task_code":"negative","target":9,"current":9,"progress":{"current":-1,"target":9}},
		{"task_code":"null","target":1,"current":1,"progress":{"current":null,"target":1}},
		{"task_code":"duplicate","target":1,"current":1,"progress":{"current":1,"current":0,"target":1}},
		{"task_code":"array","target":1,"current":1,"progress":[]},
		{"task_code":"nested_target_only","target":1,"current":1,"progress":{"target":1}},
		{"task_code":"nested_current_only","target":1,"current":1,"progress":{"current":1}},
		{"task_code":"nested_empty","target":1,"current":1,"progress":{}},
		{"task_code":"nested_extension_only","target":1,"current":1,"progress":{"source":"client"}},
		{"task_code":"flat","target":1,"current":1}
	]}`)
	tasks, err := parseGrowthTasks(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		want := task.TaskCode == "flat"
		if task.CanClaimReward() != want {
			t.Errorf("%s CanClaimReward=%v want %v (%+v)", task.TaskCode, task.CanClaimReward(), want, task)
		}
	}
}
