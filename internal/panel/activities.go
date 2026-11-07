package panel

import (
	"net/http"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/activities"
)

// activities is a read-only catalog. It intentionally does not inspect
// accounts, call upstream services, or claim rewards.
func (p *Panel) activities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"verified_at":       activities.SourceVerifiedAt,
		"verification_type": "public_document_review",
		"activities":        activities.Catalog(time.Now()),
	})
}
