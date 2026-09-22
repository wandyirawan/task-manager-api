package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"

	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/handler"
	"github.com/wandyirawan/task-manager-api/internal/api/middleware"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
	"github.com/wandyirawan/task-manager-api/internal/repository"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// --- P7 race suite: REAL HTTP over REAL PostgreSQL ---------------------------
//
// Flagship end-to-end race suite: N goroutines fire real HTTP requests through
// a real fiber app (P2 error handler → P3 Protected → handlers) into a real
// Postgres database (one throwaway DB per package run), not in-memory fakes.
// A barrier releases all goroutines at once so contention is real.
//
// Skip policy mirrors the repository tests: if Postgres is unreachable the
// suite skips instead of failing.

const raceN = 50

var (
	raceDB     *sqlx.DB
	raceDBName string
	raceDBErr  error
)

func TestMain(m *testing.M) {
	raceDB, raceDBErr = setupRaceDB()
	code := m.Run()
	if raceDB != nil {
		raceDB.Close()
	}
	// Best-effort cleanup of the throwaway database.
	if raceDBName != "" {
		if adm, err := sqlx.Connect("pgx", raceAdminURL()); err == nil {
			dropRaceDBVia(adm, raceDBName)
			adm.Close()
		}
	}
	os.Exit(code)
}

// raceAdminURL points at the maintenance database for CREATE/DROP DATABASE.
func raceAdminURL() string {
	if v := os.Getenv("TEST_DB_URL"); v != "" {
		return v
	}
	return "postgres://tm_user:tm_pass@localhost:5432/postgres?sslmode=disable"
}

// setupRaceDB creates a throwaway Postgres database once per package (same
// pattern as the repository tests), applies the embedded migrations, and
// returns a pooled handle. (nil, err) means unreachable → whole suite skips.
func setupRaceDB() (*sqlx.DB, error) {
	base := raceAdminURL()

	adm, err := sqlx.Connect("pgx", base)
	if err != nil {
		return nil, err
	}
	defer adm.Close()

	name := fmt.Sprintf("tmapi_race_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := adm.ExecContext(context.Background(),
		`CREATE DATABASE "`+name+`"`); err != nil {
		return nil, err
	}
	raceDBName = name

	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u.Path = "/" + name

	db, err := sqlx.Connect("pgx", u.String())
	if err != nil {
		dropRaceDBVia(adm, name)
		return nil, err
	}
	// Generous pool: 50-goroutine bursts must never starve on connections.
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(30)

	// Embedded migrations via the production fail-fast helper — no manual
	// migrate runs allowed (PROGRESS.md rule).
	if err := infra.RunMigrations(db); err != nil {
		db.Close()
		dropRaceDBVia(adm, name)
		return nil, err
	}
	return db, nil
}

// dropRaceDBVia removes the throwaway database through an admin connection.
func dropRaceDBVia(adm *sqlx.DB, name string) {
	_, _ = adm.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
}

// truncateAll clears every table so each test starts from a clean slate.
func truncateAll(t *testing.T) {
	t.Helper()
	if _, err := raceDB.ExecContext(context.Background(),
		`TRUNCATE TABLE users, tasks, task_logs, idempotency_keys RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// seedUser inserts a user row directly (cheaper than going through
// /register) and returns its ID plus a ready-to-use bearer token.
func seedUser(t *testing.T, email string) (string, string) {
	t.Helper()
	id := uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := raceDB.ExecContext(context.Background(),
		`INSERT INTO users (id, email, password_hash, created_at, updated_at)
		 VALUES ($1, $2, $3, NOW(), NOW())`, id, email, string(hash)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	jwt := infra.NewJWTService("race-test-secret", time.Hour)
	tok, err := jwt.GenerateToken(id)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return id, tok
}

// newRaceApp wires the production fiber app (P2 error handler + middleware
// chain + Protected P3 + task routes) onto the shared test database.
func newRaceApp() *fiber.App {
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwtSvc := infra.NewJWTService("race-test-secret", time.Hour)
	taskSvc := service.NewTaskService(repository.NewTaskRepository(raceDB), lg, nil)

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})
	app.Use(middleware.RequestID())
	app.Use(middleware.Recover(lg))
	app.Use(middleware.Protected(jwtSvc))
	group := app.Group("/tasks")
	handler.RegisterRoutes(group, handler.NewTaskHandler(taskSvc))
	return app
}

// startRaceServer mounts the app on a REAL listener (real TCP, real
// client-server split) on an OS-assigned port, and returns the base URL once
// the port actually accepts connections.
func startRaceServer(t *testing.T) string {
	t.Helper()
	app := newRaceApp()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Listener(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	base := "http://" + ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return base
}

// raceBody is the shared create payload for the concurrent cases.
func raceBody() string {
	return `{"title":"race test","description":"P7 flagship"}`
}

// postTaskWithKey issues one idempotent create against base.
func postTaskWithKey(t *testing.T, base, tok, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", base+"/tasks", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// countRows is a tiny helper for DB assertions.
func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := raceDB.GetContext(context.Background(), &n, query, args...); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// --- the suite ---------------------------------------------------------------

// TestRace_IdempotencySequential covers the basic contract before the
// concurrent cases: fresh key → 201; same key → 201 with a byte-identical
// body; exactly one task and one key row in the DB.
func TestRace_IdempotencySequential(t *testing.T) {
	if raceDBErr != nil {
		t.Skipf("Postgres unreachable: %v", raceDBErr)
	}
	truncateAll(t)
	base := startRaceServer(t)
	_, tok := seedUser(t, "seq@race.test")

	key := uuid.NewString()
	status1, body1 := postTaskWithKey(t, base, tok, key, raceBody())
	status2, body2 := postTaskWithKey(t, base, tok, key, raceBody())

	if status1 != fiber.StatusCreated || status2 != fiber.StatusCreated {
		t.Fatalf("statuses = %d/%d, want 201/201", status1, status2)
	}
	if body1 != body2 {
		t.Errorf("replay body differs:\n first=%s\nsecond=%s", body1, body2)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM idempotency_keys`); n != 1 {
		t.Errorf("keys = %d, want 1", n)
	}
}

// TestRace_IdempotencyConcurrent50 is the headline: 50 goroutines, one key,
// one body, released together by a barrier. Every response must be 201 and
// byte-identical, and the DB must hold exactly one task and one key.
func TestRace_IdempotencyConcurrent50(t *testing.T) {
	if raceDBErr != nil {
		t.Skipf("Postgres unreachable: %v", raceDBErr)
	}
	truncateAll(t)
	base := startRaceServer(t)
	_, tok := seedUser(t, "conc@race.test")

	key := uuid.NewString()
	type result struct {
		status int
		body   string
		err    error
	}
	results := make([]result, raceN)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(raceN)
	for i := 0; i < raceN; i++ {
		go func(i int) {
			defer wg.Done()
			<-start // barrier: everyone waits here
			req, err := http.NewRequest("POST", base+"/tasks", strings.NewReader(raceBody()))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Idempotency-Key", key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			results[i] = result{status: resp.StatusCode, body: string(raw)}
		}(i)
	}
	close(start) // release all 50 at once — no sleeps, no jitter
	wg.Wait()

	first := results[0]
	if first.err != nil {
		t.Fatalf("first request errored: %v", first.err)
	}
	if first.status != fiber.StatusCreated {
		t.Fatalf("first status = %d, want 201 (body %s)", first.status, first.body)
	}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: %v", i, r.err)
			continue
		}
		if r.status != fiber.StatusCreated {
			t.Errorf("goroutine %d: status = %d, want 201 (body %s)", i, r.status, r.body)
		}
		if r.body != first.body {
			t.Errorf("goroutine %d: body differs from first:\n got=%s\nwant=%s", i, r.body, first.body)
		}
	}
	if n := countRows(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want exactly 1", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM idempotency_keys`); n != 1 {
		t.Errorf("keys = %d, want exactly 1", n)
	}
}

// TestRace_IdempotencyNegativeControl proves the suite does not pass because
// of accidental over-blocking: 50 goroutines with DISTINCT keys must yield 50
// separate tasks.
func TestRace_IdempotencyNegativeControl(t *testing.T) {
	if raceDBErr != nil {
		t.Skipf("Postgres unreachable: %v", raceDBErr)
	}
	truncateAll(t)
	base := startRaceServer(t)
	_, tok := seedUser(t, "negctl@race.test")

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(raceN)
	statuses := make([]int, raceN)
	errs := make([]error, raceN)
	for i := 0; i < raceN; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			req, err := http.NewRequest("POST", base+"/tasks", strings.NewReader(raceBody()))
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Idempotency-Key", uuid.NewString()) // distinct key
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			statuses[i] = resp.StatusCode
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i, s := range statuses {
		if s != fiber.StatusCreated {
			t.Errorf("goroutine %d: status = %d, want 201", i, s)
		}
	}
	if n := countRows(t, `SELECT COUNT(*) FROM tasks`); n != raceN {
		t.Errorf("tasks = %d, want %d (distinct keys must not be blocked)", n, raceN)
	}
}

// TestRace_ReplayAfterMutation creates a task, mutates it via PUT, then
// replays the original idempotency key: the response must be the ORIGINAL
// snapshot, proving replay reads the stored snapshot rather than re-querying.
func TestRace_ReplayAfterMutation(t *testing.T) {
	if raceDBErr != nil {
		t.Skipf("Postgres unreachable: %v", raceDBErr)
	}
	truncateAll(t)
	base := startRaceServer(t)
	_, tok := seedUser(t, "replay@race.test")

	key := uuid.NewString()
	status, first := postTaskWithKey(t, base, tok, key, raceBody())
	if status != fiber.StatusCreated {
		t.Fatalf("create status = %d, want 201", status)
	}
	var env struct {
		Data domain.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(first), &env); err != nil {
		t.Fatalf("decode first: %v (body %s)", err, first)
	}

	// Mutate via PUT (status → done).
	put, err := http.NewRequest("PUT", base+"/tasks/"+env.Data.ID,
		strings.NewReader(`{"status":"done"}`))
	if err != nil {
		t.Fatalf("put request: %v", err)
	}
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("Authorization", "Bearer "+tok)
	putResp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("put status = %d, want 200", putResp.StatusCode)
	}

	// Replay the SAME key — body must equal the original snapshot.
	status, replay := postTaskWithKey(t, base, tok, key, raceBody())
	if status != fiber.StatusCreated {
		t.Fatalf("replay status = %d, want 201", status)
	}
	if replay != first {
		t.Errorf("replay differs from original snapshot:\n orig=%s\nreplay=%s", first, replay)
	}

	if n := countRows(t, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM idempotency_keys`); n != 1 {
		t.Errorf("keys = %d, want 1", n)
	}
}

// TestRace_AuthConcurrent verifies no cross-user leakage under load: 50
// goroutines, each its own user, each creating its own task with a unique
// key. Every task must land under the right owner.
func TestRace_AuthConcurrent(t *testing.T) {
	if raceDBErr != nil {
		t.Skipf("Postgres unreachable: %v", raceDBErr)
	}
	truncateAll(t)
	base := startRaceServer(t)

	userIDs := make([]string, raceN)
	tokens := make([]string, raceN)
	for i := 0; i < raceN; i++ {
		userIDs[i], tokens[i] = seedUser(t, fmt.Sprintf("user-%d@race.test", i))
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(raceN)
	errs := make([]error, raceN)
	for i := 0; i < raceN; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			req, err := http.NewRequest("POST", base+"/tasks", strings.NewReader(raceBody()))
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokens[i])
			req.Header.Set("Idempotency-Key", uuid.NewString())
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != fiber.StatusCreated {
				errs[i] = fmt.Errorf("user %d: status %d", i, resp.StatusCode)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	// Each user must own exactly one task.
	for _, uid := range userIDs {
		if n := countRows(t, `SELECT COUNT(*) FROM tasks WHERE owner_id = $1`, uid); n != 1 {
			t.Errorf("user %s owns %d tasks, want 1", uid[:8], n)
		}
	}
}
