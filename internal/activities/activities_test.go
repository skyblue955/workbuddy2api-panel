package activities

import (
	"errors"
	"testing"
	"time"
)

func officialActivity() Activity {
	return Activity{ID: "credit_redemption", Enabled: true, Support: SupportOfficial, RewardAvailable: true}
}

func TestRegistryIsSourceBackedAndStable(t *testing.T) {
	got := Registry()
	if len(got) != 5 || got[0].ID != "credit_redemption" || got[1].ID != "checkin_status" || got[2].ID != "compensation_status" || got[3].ID != "referral_fission_rules" || got[4].ID != "enterprise_doublecredits_2026" {
		t.Fatalf("registry IDs = %#v", got)
	}
	if got[0].DocsURL != DocsURL || got[0].Support != SupportOfficial {
		t.Fatalf("credit support metadata = %#v", got[0])
	}
	for _, activity := range got {
		if activity.RewardAvailable {
			t.Fatalf("legacy activity metadata = %#v", activity)
		}
	}
	if got[3].Mode != "rules_only" || got[4].Enabled || got[4].ExpiresAt == nil {
		t.Fatalf("informational metadata = %#v %#v", got[3], got[4])
	}
}

func TestDecideActive(t *testing.T) {
	got := Decide(officialActivity(), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), KnownEligibility(true), false)
	if !got.Allowed || got.Reason != DecisionAllowed {
		t.Fatalf("decision = %#v", got)
	}
}

func TestDecideExpiryBoundary(t *testing.T) {
	expires := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	activity := officialActivity()
	activity.ExpiresAt = &expires
	if got := Decide(activity, expires.Add(-time.Nanosecond), KnownEligibility(true), false); !got.Allowed {
		t.Fatalf("before expiry = %#v", got)
	}
	if got := Decide(activity, expires, KnownEligibility(true), false); got.Reason != DecisionExpired {
		t.Fatalf("at expiry = %#v", got)
	}
}

func TestDecideEligibilityStatesAndClaim(t *testing.T) {
	cases := []struct {
		name        string
		eligibility Eligibility
		claimed     bool
		reason      DecisionReason
	}{
		{"unknown", UnknownEligibility(), false, DecisionUnknownEligibility},
		{"error", EligibilityFailure(errors.New("fixture")), false, DecisionEligibilityError},
		{"claimed", KnownEligibility(true), true, DecisionAlreadyClaimed},
		{"ineligible", KnownEligibility(false), false, DecisionNoReward},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(officialActivity(), time.Time{}, tc.eligibility, tc.claimed)
			if got.Reason != tc.reason || got.Allowed {
				t.Fatalf("decision = %#v", got)
			}
			if tc.name == "error" && got.Err == nil {
				t.Fatal("eligibility error was not preserved")
			}
		})
	}
}

func TestDecideDisabledAndUnsupported(t *testing.T) {
	activity := officialActivity()
	activity.Enabled = false
	if got := Decide(activity, time.Time{}, KnownEligibility(true), false); got.Reason != DecisionDisabled {
		t.Fatalf("disabled = %#v", got)
	}
	activity.Enabled = true
	activity.Support = SupportLegacyUnverified
	if got := Decide(activity, time.Time{}, KnownEligibility(true), false); got.Reason != DecisionUnsupported {
		t.Fatalf("unsupported = %#v", got)
	}
}
