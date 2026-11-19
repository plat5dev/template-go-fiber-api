package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLifecycleConsumerEventsAndCursor(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := openDB(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// Seed tasks in two projects and two user profiles.
	ts, ps := newTaskStore(sqlDB), newProfileStore(sqlDB)
	now := timestamp(time.Now())
	for _, task := range []*Task{
		{ID: newID(), ProjectID: testProject, Title: "A", Status: "todo", CreatedBy: testMember, CreatedAt: now, UpdatedAt: now},
		{ID: newID(), ProjectID: testOtherProj, Title: "B", Status: "todo", CreatedBy: testMember, CreatedAt: now, UpdatedAt: now},
	} {
		if err := ts.insert(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ps.get(ctx, testUser, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.get(ctx, "01jd8x3k7q00000000000000u2", now); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var queries []string
	var nextStatus int
	var nextBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/internal/events" {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(nextStatus)
		_, _ = w.Write([]byte(nextBody))
	}))
	defer srv.Close()

	// Trailing slash on identityURL must be trimmed.
	consumer := newLifecycleConsumer(srv.URL+"/", sqlDB)
	consumer.retry = 10 * time.Millisecond

	// 1. Page with project purge, ignored org purge, and user deletion.
	page1, _ := json.Marshal(map[string]any{
		"events": []lifecycleEvent{
			{ID: "10", Type: "node.purged", Kind: "organization", NodeID: testOtherProj},
			{ID: "11", Type: "node.purged", Kind: "project", NodeID: testProject},
			{ID: "12", Type: "user.deleted", UserID: testUser},
		},
	})
	nextStatus, nextBody = http.StatusOK, string(page1)
	if err := consumer.step(ctx); err != nil {
		t.Fatalf("step 1: %v", err)
	}

	// testProject tasks deleted, testOtherProj kept.
	if list, _, _ := ts.list(ctx, testProject, page{limit: 10}); len(list) != 0 {
		t.Fatalf("expected purged project tasks deleted, got %d", len(list))
	}
	if list, _, _ := ts.list(ctx, testOtherProj, page{limit: 10}); len(list) != 1 {
		t.Fatalf("expected other project tasks kept, got %d", len(list))
	}
	// testUser profile deleted, u2 kept.
	var count int
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM profiles WHERE user_id = ?`, testUser).Scan(&count)
	if count != 0 {
		t.Fatalf("expected deleted user profile removed")
	}
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM profiles WHERE user_id = ?`, "01jd8x3k7q00000000000000u2").Scan(&count)
	if count != 1 {
		t.Fatalf("expected other user profile kept")
	}
	if cur, _ := consumer.cursor(ctx); cur != "12" {
		t.Fatalf("cursor = %q, want 12", cur)
	}

	// 2. 204 No Content keeps cursor and asks with after=12.
	nextStatus, nextBody = http.StatusNoContent, ""
	if err := consumer.step(ctx); err != nil {
		t.Fatalf("step 204: %v", err)
	}
	if cur, _ := consumer.cursor(ctx); cur != "12" {
		t.Fatalf("cursor after 204 = %q, want 12", cur)
	}

	// 3. 409 CURSOR_AHEAD clears cursor so next poll starts from beginning.
	nextStatus, nextBody = http.StatusConflict, `{"error":{"code":"CURSOR_AHEAD"}}`
	if err := consumer.step(ctx); err != nil {
		t.Fatalf("step 409: %v", err)
	}
	if cur, _ := consumer.cursor(ctx); cur != "" {
		t.Fatalf("cursor after 409 = %q, want empty", cur)
	}

	// 4. Error response returns error and leaves cursor untouched.
	nextStatus, nextBody = http.StatusServiceUnavailable, `{}`
	if err := consumer.step(ctx); err == nil {
		t.Fatal("expected error on 503")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 4 || queries[0] != "limit=100&types=node.purged%2Cuser.deleted" ||
		queries[1] != "after=12&limit=100&types=node.purged%2Cuser.deleted" ||
		queries[3] != "limit=100&types=node.purged%2Cuser.deleted" {
		t.Fatalf("unexpected queries: %v", queries)
	}
}

func TestLifecycleTransactionRollback(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := openDB(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// Seed a task, then drop profiles table so the second event fails mid-transaction.
	now := timestamp(time.Now())
	if err := newTaskStore(sqlDB).insert(ctx, &Task{
		ID: newID(), ProjectID: testProject, Title: "Keep", Status: "todo",
		CreatedBy: testMember, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`DROP TABLE profiles`); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []lifecycleEvent{
				{ID: "1", Type: "node.purged", Kind: "project", NodeID: testProject},
				{ID: "2", Type: "user.deleted", UserID: testUser},
			},
		})
	}))
	defer srv.Close()

	consumer := newLifecycleConsumer(srv.URL, sqlDB)
	if err := consumer.step(ctx); err == nil {
		t.Fatal("expected rollback error when profiles table missing")
	}

	// Neither the task delete nor the cursor advance should have committed.
	if list, _, _ := newTaskStore(sqlDB).list(ctx, testProject, page{limit: 10}); len(list) != 1 {
		t.Fatalf("expected task rolled back, got %d", len(list))
	}
	if cur, _ := consumer.cursor(ctx); cur != "" {
		t.Fatalf("expected cursor rolled back, got %q", cur)
	}
}
