// Package activities contains a read-only registry and eligibility decisions for
// activity surfaces. It deliberately does not perform upstream requests or
// mutate claims.
package activities

import (
	"errors"
	"time"
)

const DocsURL = "https://open.workbuddy.cn/docs/openapi"

type SupportStatus string

const (
	SupportOfficial         SupportStatus = "official"
	SupportLegacyUnverified SupportStatus = "legacy_unverified"
)

type EligibilityState string

const (
	EligibilityKnown   EligibilityState = "known"
	EligibilityUnknown EligibilityState = "unknown"
	EligibilityError   EligibilityState = "error"
)

type Eligibility struct {
	State    EligibilityState
	Eligible bool
	Err      error
}

func KnownEligibility(eligible bool) Eligibility {
	return Eligibility{State: EligibilityKnown, Eligible: eligible}
}

func UnknownEligibility() Eligibility { return Eligibility{State: EligibilityUnknown} }

func EligibilityFailure(err error) Eligibility {
	if err == nil {
		err = errors.New("eligibility lookup failed")
	}
	return Eligibility{State: EligibilityError, Err: err}
}

type Activity struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Enabled         bool          `json:"enabled"`
	Support         SupportStatus `json:"support"`
	DocsURL         string        `json:"docs_url"`
	SupportReason   string        `json:"support_reason"`
	Mode            string        `json:"mode"`
	RewardAvailable bool          `json:"reward_available"`
	StartsAt        *time.Time    `json:"starts_at,omitempty"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty"`
}

// Registry returns the small, source-backed set currently exposed to callers.
// Legacy entries describe status only; they are not an instruction to invoke
// private endpoints or to claim anything.
func Registry() []Activity {
	sep30 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*3600)) // exclusive end
	return []Activity{
		{
			ID: "credit_redemption", Name: "Credit redemption", Enabled: true,
			Support: SupportOfficial, DocsURL: DocsURL,
			SupportReason:   "official contract; requires approved app, user OAuth, and a valid code; eligibility is unknown until verified",
			RewardAvailable: false,
		},
		{
			ID: "checkin_status", Name: "Check-in status", Enabled: true,
			Support: SupportLegacyUnverified, DocsURL: DocsURL,
			SupportReason:   "legacy status surface; not verified by the current official contract",
			RewardAvailable: false,
		},
		{
			ID: "compensation_status", Name: "Compensation status", Enabled: true,
			Support: SupportLegacyUnverified, DocsURL: DocsURL,
			SupportReason:   "legacy status surface; not verified by the current official contract",
			RewardAvailable: false,
		},
		{
			ID: "referral_fission_rules", Name: "Referral fission rules", Enabled: true,
			Support: SupportOfficial, DocsURL: "https://www.codebuddy.cn/fission/", Mode: "rules_only",
			SupportReason:   "official informational referral rules only; implementation and reward entitlement are not verified",
			RewardAvailable: false,
		},
		{
			ID: "enterprise_doublecredits_2026", Name: "Enterprise double credits (historical)", Enabled: false,
			Support: SupportLegacyUnverified, DocsURL: "https://www.codebuddy.cn/docs/workbuddy/Pricing",
			SupportReason:   "historical record; expired 2026-09-30 and not an active entitlement",
			RewardAvailable: false, ExpiresAt: &sep30,
		},
	}
}

type DecisionReason string

const (
	DecisionAllowed            DecisionReason = "allowed"
	DecisionDisabled           DecisionReason = "disabled"
	DecisionUnsupported        DecisionReason = "unsupported"
	DecisionNotStarted         DecisionReason = "not_started"
	DecisionExpired            DecisionReason = "expired"
	DecisionAlreadyClaimed     DecisionReason = "already_claimed"
	DecisionUnknownEligibility DecisionReason = "unknown_eligibility"
	DecisionEligibilityError   DecisionReason = "eligibility_error"
	DecisionNoReward           DecisionReason = "no_reward"
)

type Decision struct {
	Allowed bool
	Reason  DecisionReason
	Err     error
}

// Decide evaluates a snapshot only. now is explicit so expiry boundaries are
// deterministic; no network or claim mutation occurs.
func Decide(activity Activity, now time.Time, eligibility Eligibility, claimed bool) Decision {
	if !activity.Enabled {
		return Decision{Reason: DecisionDisabled}
	}
	if activity.Support != SupportOfficial {
		return Decision{Reason: DecisionUnsupported}
	}
	if activity.StartsAt != nil && now.Before(*activity.StartsAt) {
		return Decision{Reason: DecisionNotStarted}
	}
	if activity.ExpiresAt != nil && !now.Before(*activity.ExpiresAt) {
		return Decision{Reason: DecisionExpired}
	}
	if claimed {
		return Decision{Reason: DecisionAlreadyClaimed}
	}
	switch eligibility.State {
	case EligibilityUnknown:
		return Decision{Reason: DecisionUnknownEligibility}
	case EligibilityError:
		return Decision{Reason: DecisionEligibilityError, Err: eligibility.Err}
	case EligibilityKnown:
		if !eligibility.Eligible {
			return Decision{Reason: DecisionNoReward}
		}
		if !activity.RewardAvailable {
			return Decision{Reason: DecisionNoReward}
		}
		return Decision{Allowed: true, Reason: DecisionAllowed}
	default:
		return Decision{Reason: DecisionUnknownEligibility}
	}
}
