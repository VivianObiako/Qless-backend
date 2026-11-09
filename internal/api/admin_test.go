package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vivianobiako/qless/api/internal/api"
	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/storage"
)

func adminServer(t *testing.T, adminToken string) *httptest.Server {
	t.Helper()
	url := config.TestDatabaseURL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	if err := database.Migrate(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)
	server := httptest.NewServer(api.NewServer(store).WithAdmin(adminToken).Routes("*"))
	t.Cleanup(server.Close)
	return server
}

func getAdmin(t *testing.T, server *httptest.Server, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/admin/stats", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestAdminStatsNeedsTheKey(t *testing.T) {
	server := adminServer(t, "correct-horse")

	if status, _ := getAdmin(t, server, ""); status != http.StatusUnauthorized {
		t.Fatalf("no key: status %d, want 401", status)
	}
	if status, _ := getAdmin(t, server, "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("wrong key: status %d, want 401", status)
	}

	status, body := getAdmin(t, server, "correct-horse")
	if status != http.StatusOK {
		t.Fatalf("right key: status %d, body %s", status, body)
	}
	var got struct {
		Stats struct {
			JoinsByDay []json.RawMessage `json:"joinsByDay"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Stats.JoinsByDay) != 14 {
		t.Fatalf("%d days of joins, want 14", len(got.Stats.JoinsByDay))
	}
	if strings.Contains(body, "customerName") {
		t.Fatal("admin stats must not carry customer names")
	}
}

func TestAdminStatsIsOffWithoutAKey(t *testing.T) {
	server := adminServer(t, "")
	if status, _ := getAdmin(t, server, "anything"); status != http.StatusNotFound {
		t.Fatalf("status %d, want 404 when no admin key is set", status)
	}
}
