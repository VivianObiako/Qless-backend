package database_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
	"github.com/vivianobiako/qless/api/migrations"
)

// The 00002 backfill rehomes every owner token that exists. If it is wrong,
// every dashboard link and counter tablet in the wild stops working at once,
// and the owners it locks out have no recovery code to fall back on — they
// never had one. So this test walks the real upgrade path: build the schema as
// it was before owners existed, create a queue the old way, migrate, and then
// use that same token through the code that serves the dashboard today.
func TestMigrationKeepsExistingOwnerTokensWorking(t *testing.T) {
	scratch := scratchDatabase(t)

	legacyToken, err := token.New()
	if err != nil {
		t.Fatalf("generate legacy owner token: %v", err)
	}

	queueID := seedLegacyQueue(t, scratch, token.Hash(legacyToken))

	if err := database.Migrate(scratch); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	ctx := context.Background()
	store, err := storage.New(ctx, scratch)
	if err != nil {
		t.Fatalf("connect to migrated database: %v", err)
	}
	t.Cleanup(store.Close)

	actor, err := store.ResolveActor(ctx, token.Hash(legacyToken))
	if err != nil {
		t.Fatalf("legacy owner token no longer resolves: %v", err)
	}
	if actor.Type != queue.PrincipalOwner {
		t.Errorf("actor type = %q, want OWNER", actor.Type)
	}
	if actor.OwnerID == "" {
		t.Error("migrated actor has no owner id")
	}

	if err := store.AuthorizeQueue(ctx, actor, queueID); err != nil {
		t.Fatalf("legacy owner cannot reach their own queue after migrating: %v", err)
	}

	queues, err := store.QueuesForActor(ctx, actor)
	if err != nil {
		t.Fatalf("list queues for migrated owner: %v", err)
	}
	if len(queues) != 1 || queues[0].ID != queueID {
		t.Fatalf("migrated owner holds %d queues, want exactly the one they created", len(queues))
	}

	// Staff seeing names has to be opt-in, including for queues that predate
	// the setting.
	var showNames bool
	if err := store.Pool().QueryRow(ctx,
		`SELECT show_names_to_operators FROM queues WHERE id = $1`, queueID,
	).Scan(&showNames); err != nil {
		t.Fatalf("read names setting: %v", err)
	}
	if showNames {
		t.Error("show_names_to_operators backfilled to true; existing queues must default to off")
	}
}

// Two queues sharing an owner token were always one holder — that token opened
// both dashboards. The migration has to land them in one business rather than
// fail on the unique constraint or split them apart.
func TestMigrationCollapsesQueuesSharingOneToken(t *testing.T) {
	scratch := scratchDatabase(t)

	sharedToken, err := token.New()
	if err != nil {
		t.Fatalf("generate owner token: %v", err)
	}

	first := seedLegacyQueue(t, scratch, token.Hash(sharedToken))
	second := seedLegacyQueue(t, scratch, token.Hash(sharedToken))

	if err := database.Migrate(scratch); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	ctx := context.Background()
	store, err := storage.New(ctx, scratch)
	if err != nil {
		t.Fatalf("connect to migrated database: %v", err)
	}
	t.Cleanup(store.Close)

	actor, err := store.ResolveActor(ctx, token.Hash(sharedToken))
	if err != nil {
		t.Fatalf("shared owner token no longer resolves: %v", err)
	}

	for _, id := range []string{first, second} {
		if err := store.AuthorizeQueue(ctx, actor, id); err != nil {
			t.Errorf("queue %s is no longer reachable with the token that opened it: %v", id, err)
		}
	}

	queues, err := store.QueuesForActor(ctx, actor)
	if err != nil {
		t.Fatalf("list queues: %v", err)
	}
	if len(queues) != 2 {
		t.Errorf("owner holds %d queues, want both", len(queues))
	}
}

// Every queue that existed before seats gets one, "Counter", and whoever was
// at that counter is pointed at it. Otherwise the per-seat serving index
// would let a second person be called on a queue whose one occupant has no
// seat, and history would have no chair for anybody served before today.
func TestMigrationGivesEveryQueueOneSeat(t *testing.T) {
	scratch := scratchDatabase(t)

	ownerToken, err := token.New()
	if err != nil {
		t.Fatalf("generate owner token: %v", err)
	}
	queueID := seedLegacyQueue(t, scratch, token.Hash(ownerToken))

	db := openDatabase(t, scratch)
	if _, err := db.Exec(
		`INSERT INTO queue_entries (queue_id, number, customer_name, customer_token_hash, status, started_at)
		 VALUES ($1, 1, 'Served', 'hash-1', 'ATTENDED', now()),
		        ($1, 2, 'At the counter', 'hash-2', 'SERVING', now()),
		        ($1, 3, 'Waiting', 'hash-3', 'WAITING', NULL)`,
		queueID,
	); err != nil {
		t.Fatalf("seed legacy entries: %v", err)
	}
	_ = db.Close()

	if err := database.Migrate(scratch); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	ctx := context.Background()
	store, err := storage.New(ctx, scratch)
	if err != nil {
		t.Fatalf("connect to migrated database: %v", err)
	}
	t.Cleanup(store.Close)

	seats, err := store.Seats(ctx, queueID)
	if err != nil {
		t.Fatalf("list seats: %v", err)
	}
	if len(seats) != 1 || seats[0].Name != storage.DefaultSeatName || !seats[0].Active {
		t.Fatalf("migrated queue has seats %+v, want one open %q", seats, storage.DefaultSeatName)
	}

	rows, err := store.Pool().Query(ctx,
		`SELECT number, seat_id IS NOT NULL FROM queue_entries WHERE queue_id = $1 ORDER BY number`, queueID,
	)
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	defer rows.Close()
	seated := map[int]bool{}
	for rows.Next() {
		var number int
		var hasSeat bool
		if err := rows.Scan(&number, &hasSeat); err != nil {
			t.Fatalf("scan entry: %v", err)
		}
		seated[number] = hasSeat
	}
	if !seated[1] || !seated[2] || seated[3] {
		t.Fatalf("seat backfill = %v, want the called entries on the counter and the waiting one on none", seated)
	}

	// The queue still serves as before: the one seat is reused, and the
	// person on it is stood down.
	result, err := store.ServeNext(ctx, queueID, "", queue.Actor{Type: queue.PrincipalOwner})
	if err != nil {
		t.Fatalf("serve next after migrating: %v", err)
	}
	if result.Served == nil || result.Served.Number != 3 || result.Attended == nil || result.Attended.Number != 2 {
		t.Fatalf("serve next = %+v, want #3 called and #2 stood down", result)
	}
}

// seedLegacyQueue writes a queue in the 00001 schema — one token hash on the
// row, no owner anywhere — and returns its id.
func seedLegacyQueue(t *testing.T, databaseURL, ownerTokenHash string) string {
	t.Helper()

	db := openDatabase(t, databaseURL)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 1); err != nil {
		t.Fatalf("migrate to 00001: %v", err)
	}

	slug := fmt.Sprintf("legacy-%d", time.Now().UnixNano())

	var queueID string
	err := db.QueryRow(
		`INSERT INTO queues (name, slug, average_service_minutes, owner_token_hash)
		 VALUES ('Legacy Barbershop', $1, 15, $2) RETURNING id`,
		slug, ownerTokenHash,
	).Scan(&queueID)
	if err != nil {
		t.Fatalf("insert legacy queue: %v", err)
	}
	return queueID
}

// scratchDatabase gives the test its own database to migrate from empty. The
// shared test database is already at head and cannot be walked forward again.
func scratchDatabase(t *testing.T) string {
	t.Helper()

	base := config.TestDatabaseURL()
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set; run `make up` and copy .env.example to .env")
	}

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}

	name := fmt.Sprintf("qless_migration_%d", time.Now().UnixNano())

	admin := openDatabase(t, base)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		_ = admin.Close()
		t.Skipf("cannot create a scratch database (%v); this test needs a role that may CREATE DATABASE", err)
	}

	t.Cleanup(func() {
		// FORCE drops the connections the migration left behind rather than
		// leaving a database per test run lying around.
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
		_ = admin.Close()
	})

	parsed.Path = "/" + name
	return parsed.String()
}

func openDatabase(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	return db
}
