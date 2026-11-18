package idempotency

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	clean := func() {
		if _, err := pool.Exec(context.Background(),
			`TRUNCATE idempotency_keys, links, users RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	clean()
	t.Cleanup(func() { clean(); pool.Close() })
	return pool
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

type tokens map[string]int64

func (f tokens) ResolveAPIToken(_ context.Context, token, _ string, _ time.Time) (auth.APITokenUser, error) {
	id, ok := f[token]
	if !ok {
		return auth.APITokenUser{}, auth.ErrAPITokenInvalid
	}
	return auth.APITokenUser{SessionUser: auth.SessionUser{ID: id}, TokenID: id, TokenName: "t"}, nil
}

type noSessions struct{}

func (noSessions) ResolveSession(context.Context, string, time.Time) (auth.SessionUser, error) {
	return auth.SessionUser{}, auth.ErrSessionInvalid
}

// fixture is the middleware in front of a handler that inserts a link per call
// (key taken from the body) so replays can be verified by counting rows.
type fixture struct {
	pool    *pgxpool.Pool
	srv     http.Handler
	calls   atomic.Int32
	status  int // status the handler answers with (default 201)
	gate    chan struct{}
	entered chan struct{}
	alice   int64
	bob     int64
}

func newFixture(t *testing.T) *fixture {
	pool := testPool(t)
	f := &fixture{pool: pool, status: http.StatusCreated}
	f.alice = seedUser(t, pool, "alice@example.com")
	f.bob = seedUser(t, pool, "bob@example.com")

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.entered != nil {
			f.entered <- struct{}{}
			<-f.gate
		}
		b, _ := io.ReadAll(r.Body)
		u, _ := middleware.UserFromContext(r.Context())
		if f.status < 300 {
			if _, err := pool.Exec(r.Context(),
				`INSERT INTO links (user_id, key, destination_url) VALUES ($1, $2, 'https://e.example/')`,
				u.ID, strings.TrimSpace(string(b))); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/api/links/"+strings.TrimSpace(string(b)))
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"key":"` + strings.TrimSpace(string(b)) + `"}`))
	})
	guard := middleware.RequireSessionOrAPIToken(noSessions{},
		tokens{"slk_alice": f.alice, "slk_bob": f.bob}, middleware.APITokenLimits{})
	f.srv = guard(Middleware(NewStore(pool))(h))
	return f
}

func (f *fixture) do(token, key, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set(Header, key)
	}
	rr := httptest.NewRecorder()
	f.srv.ServeHTTP(rr, req)
	return rr
}

func (f *fixture) links(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM links`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFirstUseAndReplay(t *testing.T) {
	f := newFixture(t)
	first := f.do("slk_alice", "k1", "/api/links", "aaa")
	if first.Code != 201 || first.Header().Get(ReplayHeader) != "" {
		t.Fatalf("first = %d replayed=%q", first.Code, first.Header().Get(ReplayHeader))
	}
	second := f.do("slk_alice", "k1", "/api/links", "aaa")
	if second.Code != 201 || second.Header().Get(ReplayHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q", second.Code, second.Header().Get(ReplayHeader))
	}
	if second.Body.String() != first.Body.String() ||
		second.Header().Get("Location") != "/api/links/aaa" ||
		second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay differs: %q %v", second.Body.String(), second.Header())
	}
	if f.calls.Load() != 1 || f.links(t) != 1 {
		t.Fatalf("handler calls=%d links=%d; want 1,1", f.calls.Load(), f.links(t))
	}
}

func TestNoHeaderUnchanged(t *testing.T) {
	f := newFixture(t)
	f.do("slk_alice", "", "/api/links", "a1")
	f.do("slk_alice", "", "/api/links", "a2")
	if f.calls.Load() != 2 || f.links(t) != 2 {
		t.Fatalf("calls=%d links=%d; want 2,2", f.calls.Load(), f.links(t))
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys`).Scan(&n)
	if n != 0 {
		t.Fatalf("idempotency rows = %d; want 0", n)
	}
}

func TestBodyOrPathMismatch(t *testing.T) {
	f := newFixture(t)
	f.do("slk_alice", "k1", "/api/links", "aaa")
	for name, rr := range map[string]*httptest.ResponseRecorder{
		"body": f.do("slk_alice", "k1", "/api/links", "bbb"),
		"path": f.do("slk_alice", "k1", "/api/campaigns", "aaa"),
	} {
		if rr.Code != 422 || !strings.Contains(rr.Body.String(), `"idempotency_key_reused"`) {
			t.Fatalf("%s mismatch = %d %s", name, rr.Code, rr.Body.String())
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d; want 1", f.calls.Load())
	}
}

func TestInProgress(t *testing.T) {
	f := newFixture(t)
	f.entered, f.gate = make(chan struct{}, 1), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var first *httptest.ResponseRecorder
	go func() { defer wg.Done(); first = f.do("slk_alice", "k1", "/api/links", "aaa") }()
	<-f.entered // first request is inside the handler, row is pending

	rr := f.do("slk_alice", "k1", "/api/links", "aaa")
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), `"idempotency_key_in_progress"`) {
		t.Fatalf("in-flight = %d %s", rr.Code, rr.Body.String())
	}
	close(f.gate)
	wg.Wait()
	if first.Code != 201 {
		t.Fatalf("first = %d", first.Code)
	}
	if f.calls.Load() != 1 || f.links(t) != 1 {
		t.Fatalf("calls=%d links=%d; want 1,1", f.calls.Load(), f.links(t))
	}
}

func TestConcurrentSameKeyRunsOnce(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i] = f.do("slk_alice", "k1", "/api/links", "aaa").Code }()
	}
	wg.Wait()
	if f.calls.Load() != 1 || f.links(t) != 1 {
		t.Fatalf("calls=%d links=%d codes=%v; want 1 run", f.calls.Load(), f.links(t), codes)
	}
	for _, c := range codes {
		if c != 201 && c != 409 {
			t.Fatalf("unexpected code in %v", codes)
		}
	}
}

func TestServerErrorNotStored(t *testing.T) {
	f := newFixture(t)
	f.status = 500
	if rr := f.do("slk_alice", "k1", "/api/links", "aaa"); rr.Code != 500 {
		t.Fatalf("code = %d", rr.Code)
	}
	f.status = 201
	rr := f.do("slk_alice", "k1", "/api/links", "aaa")
	if rr.Code != 201 || rr.Header().Get(ReplayHeader) != "" {
		t.Fatalf("retry after 5xx = %d replayed=%q", rr.Code, rr.Header().Get(ReplayHeader))
	}
	if f.calls.Load() != 2 {
		t.Fatalf("calls = %d; want 2", f.calls.Load())
	}
}

func TestClientErrorIsReplayed(t *testing.T) {
	f := newFixture(t)
	f.status = 400
	f.do("slk_alice", "k1", "/api/links", "aaa")
	rr := f.do("slk_alice", "k1", "/api/links", "aaa")
	if rr.Code != 400 || rr.Header().Get(ReplayHeader) != "true" || f.calls.Load() != 1 {
		t.Fatalf("4xx replay = %d %q calls=%d", rr.Code, rr.Header().Get(ReplayHeader), f.calls.Load())
	}
}

func TestPanicReleasesKey(t *testing.T) {
	pool := testPool(t)
	uid := seedUser(t, pool, "alice@example.com")
	store := NewStore(pool)
	boom := true
	h := Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if boom {
			panic("boom")
		}
		w.WriteHeader(201)
	}))
	guard := middleware.RequireSessionOrAPIToken(noSessions{}, tokens{"slk_a": uid}, middleware.APITokenLimits{})(h)
	call := func() (code int, panicked bool) {
		defer func() { panicked = recover() != nil }()
		req := httptest.NewRequest("POST", "/api/links", strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer slk_a")
		req.Header.Set(Header, "k")
		rr := httptest.NewRecorder()
		guard.ServeHTTP(rr, req)
		return rr.Code, false
	}
	if _, p := call(); !p {
		t.Fatal("expected panic to propagate")
	}
	boom = false
	if code, p := call(); p || code != 201 {
		t.Fatalf("retry after panic = %d panicked=%v", code, p)
	}
}

func TestPerUserIsolation(t *testing.T) {
	f := newFixture(t)
	f.do("slk_alice", "same", "/api/links", "a1")
	rr := f.do("slk_bob", "same", "/api/links", "b1")
	if rr.Code != 201 || rr.Header().Get(ReplayHeader) != "" {
		t.Fatalf("bob = %d replayed=%q", rr.Code, rr.Header().Get(ReplayHeader))
	}
	if f.calls.Load() != 2 || f.links(t) != 2 {
		t.Fatalf("calls=%d links=%d; want 2,2", f.calls.Load(), f.links(t))
	}
}

func TestKeyValidation(t *testing.T) {
	f := newFixture(t)
	bad := []string{strings.Repeat("a", 256), "café", "tab\there", " \x7f"}
	for _, k := range bad {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/links", strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer slk_alice")
		req.Header[Header] = []string{k}
		f.srv.ServeHTTP(rr, req)
		if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid_idempotency_key") {
			t.Errorf("key %q = %d %s", k, rr.Code, rr.Body.String())
		}
	}
	// Empty header value, and the 255 / printable boundaries.
	req := httptest.NewRequest("POST", "/api/links", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer slk_alice")
	req.Header[Header] = []string{""}
	rr := httptest.NewRecorder()
	f.srv.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("empty key = %d", rr.Code)
	}
	if rr := f.do("slk_alice", strings.Repeat("a", 255), "/api/links", "ok1"); rr.Code != 201 {
		t.Errorf("255-char key = %d", rr.Code)
	}
	if rr := f.do("slk_alice", "~ !", "/api/links", "ok2"); rr.Code != 201 {
		t.Errorf("printable key = %d", rr.Code)
	}
	if f.calls.Load() != 2 {
		t.Errorf("calls = %d; want 2 (invalid keys never reach handler)", f.calls.Load())
	}
}

func TestExpiryAndPrune(t *testing.T) {
	f := newFixture(t)
	f.do("slk_alice", "k1", "/api/links", "aaa")
	f.do("slk_alice", "old", "/api/links", "bbb")
	// Age "k1" past the TTL: it is treated as absent and processed afresh.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET created_at = now() - interval '25 hours' WHERE key = 'k1'`); err != nil {
		t.Fatal(err)
	}
	// Prune removes it (and only it).
	if n, err := NewStore(f.pool).Prune(context.Background(), time.Now()); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1", n, err)
	}
	rr := f.do("slk_alice", "k1", "/api/links", "ccc")
	if rr.Code != 201 || rr.Header().Get(ReplayHeader) != "" {
		t.Fatalf("after expiry = %d replayed=%q", rr.Code, rr.Header().Get(ReplayHeader))
	}
	// An expired row that was NOT yet pruned is also treated as absent, even
	// with a different body (no 422).
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET created_at = now() - interval '25 hours' WHERE key = 'old'`); err != nil {
		t.Fatal(err)
	}
	if rr := f.do("slk_alice", "old", "/api/links", "ddd"); rr.Code != 201 {
		t.Fatalf("unpruned expired row = %d %s", rr.Code, rr.Body.String())
	}
}

func TestStalePendingReclaimed(t *testing.T) {
	f := newFixture(t)
	pool := f.pool
	store := NewStore(pool)
	hash := []byte("h")
	now := time.Now()
	if c, err := store.Claim(context.Background(), f.alice, "k", "POST", "/p", hash, now.Add(-2*StalePending)); err != nil || c.Outcome != Owned {
		t.Fatalf("seed claim = %+v %v", c, err)
	}
	// Fresh pending: in progress. Stale pending: reclaimed by exactly one caller.
	if c, _ := store.Claim(context.Background(), f.alice, "k", "POST", "/p", hash, now.Add(-2*StalePending+time.Second)); c.Outcome != InProgress {
		t.Fatalf("fresh pending = %v", c.Outcome)
	}
	var owned atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, _ := store.Claim(context.Background(), f.alice, "k", "POST", "/p", hash, now); c.Outcome == Owned {
				owned.Add(1)
			}
		}()
	}
	wg.Wait()
	if owned.Load() != 1 {
		t.Fatalf("reclaimers = %d; want 1", owned.Load())
	}
	// A stale row with a different hash is still a mismatch, never reclaimed.
	if c, _ := store.Claim(context.Background(), f.alice, "k", "POST", "/p", []byte("other"), now); c.Outcome != Reused {
		t.Fatalf("mismatch = %v", c.Outcome)
	}
}

// A stale owner that finishes after another request reclaimed the key must
// neither overwrite nor delete the new owner's stored response.
func TestStaleOwnerIsFenced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := NewStore(f.pool)
	hash := []byte("h")
	now := time.Now()

	a, err := store.Claim(ctx, f.alice, "k", "POST", "/p", hash, now.Add(-2*StalePending))
	if err != nil || a.Outcome != Owned || a.ClaimID == "" {
		t.Fatalf("A claim = %+v %v", a, err)
	}
	b, err := store.Claim(ctx, f.alice, "k", "POST", "/p", hash, now) // A is stale
	if err != nil || b.Outcome != Owned || b.ClaimID == a.ClaimID {
		t.Fatalf("B reclaim = %+v %v", b, err)
	}
	if ok, err := store.Complete(ctx, f.alice, "k", b.ClaimID, Stored{Status: 201, Body: []byte("B")}); err != nil || !ok {
		t.Fatalf("B complete = %v %v", ok, err)
	}
	// A finishes late.
	if ok, _ := store.Complete(ctx, f.alice, "k", a.ClaimID, Stored{Status: 201, Body: []byte("A")}); ok {
		t.Fatal("stale A overwrote B's response")
	}
	if ok, _ := store.Release(ctx, f.alice, "k", a.ClaimID); ok {
		t.Fatal("stale A released B's row")
	}
	c, err := store.Claim(ctx, f.alice, "k", "POST", "/p", hash, now)
	if err != nil || c.Outcome != Replay || string(c.Stored.Body) != "B" {
		t.Fatalf("replay = %+v %v; want B's response", c, err)
	}
}

// TestMigrationRoundTrip requires migrations/000018 to be applied to the test
// DB first (psql -f migrations/000018_create_idempotency_keys.up.sql); like the
// other DB tests it does not run migrations itself.
func TestMigrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	up, err := os.ReadFile("../../migrations/000018_create_idempotency_keys.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000018_create_idempotency_keys.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	exists := func() bool {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.idempotency_keys') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Leave the DB as found whatever happens.
	t.Cleanup(func() {
		if !exists() {
			_, _ = pool.Exec(ctx, string(up))
		}
	})
	if !exists() {
		t.Fatal("idempotency_keys missing: apply migrations/000018 to the test DB")
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil || exists() {
		t.Fatalf("down: err=%v exists=%v", err, exists())
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil || !exists() {
		t.Fatalf("up: err=%v exists=%v", err, exists())
	}
}
