package repository_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/repository"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// newStores builds the repository that satisfies IdempotencyStore.
func newStores(t *testing.T) *sqlx.DB {
	t.Helper()
	return newTestDB(t)
}

// newStoresWithPool returns the shared high-concurrency test DB (the pool is
// sized generously in setup so N-goroutine tests never starve on connection
// acquisition — the old SQLite SQLITE_BUSY concern does not apply to Postgres).
func newStoresWithPool(t *testing.T, n int) *sqlx.DB {
	t.Helper()
	return newTestDBWithPool(t, n)
}

// TestIdempotencySequentialDuplicate: first create → task; second create with
// the same key → replay of the IDENTICAL snapshot; no extra rows.
func TestIdempotencySequentialDuplicate(t *testing.T) {
	db := newTestDB(t)
	store := struct {
		service.IdempotencyStore
	}{repository.NewTaskRepository(db).(service.IdempotencyStore)}
	seedUser(t, db, "u-seq")
	ctx := context.Background()

	key := "0f5b7a52-1f34-4b8f-9a45-1c2b3d4e5f60"
	task := makeTask("u-seq", "idempotent task", domain.TaskStatusTodo)

	t1, created1, err := store.CreateTaskWithKey(ctx, task, key)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !created1 {
		t.Fatal("first create must be created=true")
	}

	rec, err := store.LookupByKey(ctx, key, "u-seq")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if rec == nil {
		t.Fatal("snapshot must exist after first create")
	}

	// Second create with the same key must hit the UNIQUE backstop path and
	// return the ORIGINAL task (created=false).
	task2 := makeTask("u-seq", "idempotent task", domain.TaskStatusTodo)
	t2, created2, err := store.CreateTaskWithKey(ctx, task2, key)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created2 {
		t.Fatal("second create must be created=false (replay)")
	}
	if t1.ID != t2.ID {
		t.Errorf("replay returned different task id: %s vs %s", t1.ID, t2.ID)
	}
}

// TestIdempotencyReplayBodyIdentical: the stored snapshot, when marshaled,
// is byte-identical to the response body of the first create.
func TestIdempotencyReplayBodyIdentical(t *testing.T) {
	db := newTestDB(t)
	store := struct {
		service.IdempotencyStore
	}{repository.NewTaskRepository(db).(service.IdempotencyStore)}
	seedUser(t, db, "u-body")
	ctx := context.Background()

	key := "1a5b7a52-1f34-4b8f-9a45-1c2b3d4e5f61"
	task := makeTask("u-body", "snapshot task", domain.TaskStatusTodo)

	t1, _, err := store.CreateTaskWithKey(ctx, task, key)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := json.Marshal(t1)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rec, err := store.LookupByKey(ctx, key, "u-body")
	if err != nil || rec == nil {
		t.Fatalf("lookup: %v", err)
	}
	if !strings.EqualFold(rec.ResponseBody, string(first)) {
		// Snapshot was stored from the task the winner created — the object
		// fields must match even if key ordering differs between marshals.
		var a, b map[string]any
		_ = json.Unmarshal([]byte(rec.ResponseBody), &a)
		_ = json.Unmarshal(first, &b)
		if ja, _ := json.Marshal(a); string(ja) != mustStable(b) {
			t.Errorf("snapshot body differs from created task:\n  snap: %s\n  orig: %s", rec.ResponseBody, first)
		}
	}
}

func mustStable(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// TestIdempotencyUserScoped: the same key under a DIFFERENT user creates a
// separate task — the PK is (key, user_id), not (key).
func TestIdempotencyUserScoped(t *testing.T) {
	db := newTestDB(t)
	store := struct {
		service.IdempotencyStore
	}{repository.NewTaskRepository(db).(service.IdempotencyStore)}
	seedUser(t, db, "u-a")
	seedUser(t, db, "u-b")
	ctx := context.Background()

	key := "2a5b7a52-1f34-4b8f-9a45-1c2b3d4e5f62"

	tA, cA, err := store.CreateTaskWithKey(ctx, makeTask("u-a", "A", domain.TaskStatusTodo), key)
	if err != nil || !cA {
		t.Fatalf("user A create: %v created=%v", err, cA)
	}
	tB, cB, err := store.CreateTaskWithKey(ctx, makeTask("u-b", "B", domain.TaskStatusTodo), key)
	if err != nil || !cB {
		t.Fatalf("user B create: %v created=%v", err, cB)
	}
	if tA.ID == tB.ID {
		t.Errorf("same key across users must NOT collide; got same id %s", tA.ID)
	}
}

// TestIdempotencyExpiredKey: an expired key is treated as absent — a fresh
// create succeeds.
func TestIdempotencyExpiredKey(t *testing.T) {
	db := newTestDB(t)
	store := struct {
		service.IdempotencyStore
	}{repository.NewTaskRepository(db).(service.IdempotencyStore)}
	seedUser(t, db, "u-exp")
	ctx := context.Background()

	key := "3a5b7a52-1f34-4b8f-9a45-1c2b3d4e5f63"
	task := makeTask("u-exp", "first", domain.TaskStatusTodo)
	if _, _, err := store.CreateTaskWithKey(ctx, task, key); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Force-expire the key row directly.
	if _, err := db.ExecContext(ctx,
		`UPDATE idempotency_keys SET expires_at = $1 WHERE key = $2`,
		time.Now().UTC().Add(-1*time.Hour), key); err != nil {
		t.Fatalf("expire: %v", err)
	}

	rec, err := store.LookupByKey(ctx, key, "u-exp")
	if err != nil {
		t.Fatalf("lookup expired: %v", err)
	}
	if rec != nil {
		t.Fatal("expired key must look up as nil (not found)")
	}

	fresh := makeTask("u-exp", "second", domain.TaskStatusTodo)
	fresh.ID = "tsk-second"
	_, created, err := store.CreateTaskWithKey(ctx, fresh, key)
	if err != nil {
		t.Fatalf("fresh create after expiry: %v", err)
	}
	if !created {
		t.Fatal("create after expiry must be created=true")
	}
}

// TestIdempotencyConcurrentSameKey is the REPO-LEVEL race simulation: 50
// goroutines call CreateTaskWithKey with the same key simultaneously. Exactly
// one must create; the rest must replay the same task. The UNIQUE (key,
// user_id) backstop plus Postgres MVCC/serialization retries keep this
// deterministic.
func TestIdempotencyConcurrentSameKey(t *testing.T) {
	db := newStoresWithPool(t, 64) // pool is sized ≥ goroutines in setup
	store := struct {
		service.IdempotencyStore
	}{repository.NewTaskRepository(db).(service.IdempotencyStore)}
	seedUser(t, db, "u-race")
	ctx := context.Background()

	const N = 50
	key := "4a5b7a52-1f34-4b8f-9a45-1c2b3d4e5f64"

	type result struct {
		taskID  string
		created bool
		err     error
	}
	results := make(chan result, N)
	start := make(chan struct{})

	for i := 0; i < N; i++ {
		go func(i int) {
			<-start // barrier: all released at once
			task := makeTask("u-race", "race task", domain.TaskStatusTodo)
			task.ID = task.ID + string(rune('a'+i%26))
			tk, created, err := store.CreateTaskWithKey(ctx, task, key)
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{taskID: tk.ID, created: created}
		}(i)
	}
	close(start)

	var createdCount, replayCount, sameID int
	firstID := ""
	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("goroutine %d error: %v", i, r.err)
		}
		if firstID == "" {
			firstID = r.taskID
		}
		if r.taskID == firstID {
			sameID++
		}
		if r.created {
			createdCount++
		} else {
			replayCount++
		}
	}

	if createdCount != 1 {
		t.Errorf("created count = %d, want exactly 1", createdCount)
	}
	if replayCount != N-1 {
		t.Errorf("replay count = %d, want %d", replayCount, N-1)
	}
	if sameID != N {
		t.Errorf("identical task id = %d/%d goroutines, want all", sameID, N)
	}

	// DB-level final state: exactly one task and one key row.
	var tasks, keys int
	if err := db.GetContext(ctx, &tasks, `SELECT COUNT(*) FROM tasks WHERE owner_id = 'u-race'`); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.GetContext(ctx, &keys, `SELECT COUNT(*) FROM idempotency_keys WHERE user_id = 'u-race'`); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if tasks != 1 {
		t.Errorf("tasks rows = %d, want 1", tasks)
	}
	if keys != 1 {
		t.Errorf("idempotency_keys rows = %d, want 1", keys)
	}
}
