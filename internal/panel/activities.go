package panel

import (
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/activities"
)

// activities is a read-only catalog. It intentionally does not inspect
// accounts, call upstream services, or claim rewards.
func (p *Panel) activities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"verified_at": "2026-11-02",
		"activities":  activities.Registry(),
	})
}
