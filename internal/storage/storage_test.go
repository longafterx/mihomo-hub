//go:build integration

package storage_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/migrations"
	"github.com/pressly/goose/v3"
)

func TestMigrationsAndReadiness(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hub.db")
	for range 2 {
		db, err := storage.Open(t.Context(), path, 5000)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Ready(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.QueryRow("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != storage.SchemaVersion {
			t.Fatalf("schema = %d", version)
		}
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal = %s, error = %v", mode, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := storage.Ready(t.Context(), db); err == nil {
			t.Fatal("closed database reports ready")
		}
	}
}

func TestConnectionPragmasAndFutureSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := storage.Open(t.Context(), path, 1234)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	connections := []*sql.Conn{}
	for range 3 {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		var fk, busy int
		if err := conn.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 1234 {
			t.Fatalf("connection pragmas fk=%d busy=%d", fk, busy)
		}
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := storage.Open(t.Context(), path, 5000); err == nil {
		reopened.Close()
		t.Fatal("future schema was accepted")
	}
}

func TestSimplifyStorageMigrationPreservesData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema5.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	old := fstest.MapFS{}
	for _, name := range []string{
		"001_init.sql", "002_telemetry.sql", "003_active_instance_names.sql",
		"004_managed_instances.sql", "005_runtime_configs.sql",
	} {
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		old,
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "master.key")
	if err := storage.InitKey(keyPath); err != nil {
		t.Fatal(err)
	}
	aead, err := storage.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	const controller = "http://127.0.0.1:9090"
	for _, id := range []string{"active", "archived"} {
		secret, err := storage.Encrypt(aead, id, "migration-test-secret")
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(
			`INSERT INTO instances VALUES(?,?,?,?,1,1,1,2,NULL,7)`,
			id,
			id,
			controller,
			secret,
		)
		if err != nil {
			t.Fatal(err)
		}
		seedLegacyTelemetry(t, db, id)
	}
	_, err = db.Exec(`UPDATE instances SET name='active',enabled=0,collect_enabled=0,
		deleted_at_ms=62000,controller_url='',secret_ciphertext=x'' WHERE id='archived'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := (storage.Admins{DB: db}).Initialize(t.Context(), auth.Admin{
		Username: "admin", PasswordHash: "existing-password-hash",
	}); err != nil {
		t.Fatal(err)
	}
	config := runtimeconfig.Record{
		YAML: "proxies: []\nrules: []\ndns:\n  enable: true\n", ControllerURL: controller,
		Revision: 1, UpdatedAtMS: 61000,
	}
	if err := (&storage.RuntimeConfigs{DB: db, AEAD: aead}).Save(t.Context(), "active", config, 0); err != nil {
		t.Fatal(err)
	}

	// 比较全部保留字段和密文字节；不把有意删除的辅助列纳入快照。
	snapshot := func() map[string][][]any {
		out := map[string][][]any{}
		for _, table := range []string{
			"admin", "instances", "audit_events", "readiness_probe", "runtime_configs", "collector_cursor",
			"traffic_minute", "traffic_hour", "traffic_dimension_minute", "traffic_dimension_hour",
			"connection_records", "traffic_gap_intervals",
		} {
			columns := "*"
			switch table {
			case "instances":
				columns = "id,name,controller_url,secret_ciphertext,created_at_ms,updated_at_ms,deleted_at_ms,generation"
			case "collector_cursor":
				columns = "instance_id,epoch_id,source_id,upload_total,download_total,at_ms"
			}
			rows, err := db.Query("SELECT " + columns + " FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			names, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = [][]any{}
			for rows.Next() {
				values := make([]any, len(names))
				pointers := make([]any, len(names))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				out[table] = append(out[table], values)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := snapshot()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(t.Context(), path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := snapshot()
	for table, data := range before {
		if !reflect.DeepEqual(data, after[table]) {
			t.Fatalf("migration changed retained data in %s", table)
		}
	}
	if n := scalar(t, db, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('collector_state','instance_epochs')`); n != 0 {
		t.Fatal("unused auxiliary tables remain", n)
	}
	if n := scalar(t, db, `SELECT COUNT(*) FROM pragma_table_info('instances')
		WHERE name IN ('enabled','collect_enabled')`); n != 0 {
		t.Fatal("redundant management columns remain", n)
	}
	if n := scalar(t, db, `SELECT COUNT(*) FROM pragma_table_info('collector_cursor') WHERE name='sample_id'`); n != 0 {
		t.Fatal("unused sample ID remains", n)
	}
	if n := scalar(t, db, "SELECT COUNT(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Fatal("migration broke foreign keys", n)
	}
	items, err := (&storage.Instances{DB: db, AEAD: aead}).List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Secret != "migration-test-secret" || !items[0].CollectEnabled {
		t.Fatal("active instance was not restored with its original credentials")
	}
	imported, err := (&storage.RuntimeConfigs{DB: db, AEAD: aead}).Get(t.Context(), "active")
	if err != nil || imported != config {
		t.Fatalf("imported YAML was not preserved: %v", err)
	}
	cursor, err := (storage.Telemetry{DB: db}).LoadCursor(t.Context(), "active")
	if err != nil || cursor.At != 61000 || cursor.Upload != 120 || cursor.EpochID != "legacy-epoch" {
		t.Fatalf("collector baseline was not preserved: %v", err)
	}
}

// 旧版本升级测试使用当时的列结构写入夹具，避免让新版存储代码依赖已经删除的旧字段。
func seedLegacyTelemetry(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO instance_epochs VALUES(?1,'legacy-epoch',60000,'first_seen')`,
		`INSERT INTO collector_cursor VALUES(?1,'legacy-epoch','legacy-sample','controller',120,240,61000)`,
		`INSERT INTO collector_state VALUES(?1,7,'online','',61000)`,
		`INSERT INTO traffic_minute VALUES(?1,60000,20,40,10,20,1000,1)`,
		`INSERT INTO traffic_hour VALUES(?1,0,20,40,10,20,1000,1)`,
		`INSERT INTO connection_records VALUES(?1,'lifecycle','legacy-epoch','shared',NULL,60000,61000,
			NULL,'','fixture.example','DIRECT','Match:','curl','192.168.1.2',60,120)`,
		`INSERT INTO traffic_dimension_minute VALUES(?1,60000,'host','fixture.example',10,20)`,
		`INSERT INTO traffic_dimension_hour VALUES(?1,0,'host','fixture.example',10,20)`,
		`INSERT INTO traffic_gap_intervals VALUES(?1 || '-gap',?1,'legacy-epoch',60000,60000,
			'first_seen','unknown',NULL,NULL)`,
	} {
		if _, err := db.Exec(query, id); err != nil {
			t.Fatal(err)
		}
	}
}
