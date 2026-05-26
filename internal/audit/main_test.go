package audit

import (
	"os"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/testdb"
)

// TestMain serializes this package's live-DB tests against the other DB-backed
// packages via the shared advisory lock, since they all truncate the same
// shared tables. A no-op when TEST_DATABASE_URL is unset.
func TestMain(m *testing.M) {
	release := testdb.Lock()
	code := m.Run()
	release()
	os.Exit(code)
}
