package clicks

import (
	"os"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/testdb"
)

func TestMain(m *testing.M) {
	release := testdb.Lock()
	code := m.Run()
	release()
	os.Exit(code)
}
