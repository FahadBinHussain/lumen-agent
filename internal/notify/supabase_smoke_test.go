package notify

// smoke test that drives checkSupabase against the REAL lumen app_state DB
// + real supabase API. skipped by default (needs LUMEN_SMOKE=1). run:
//   $env:LUMEN_SMOKE="1"; go test ./internal/notify -run TestSupabaseSmoke -v
import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSupabaseSmoke(t *testing.T) {
	if os.Getenv("LUMEN_SMOKE") != "1" {
		t.Skip("set LUMEN_SMOKE=1 to run the real-API smoke test")
	}
	dsn := os.Getenv("LUMEN_DATABASE_URL")
	if dsn == "" {
		b, _ := os.ReadFile("C:/Users/Admin/Downloads/automata/supabase.com/.env.local")
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "LUMEN_DATABASE_URL=") {
				dsn = strings.Trim(strings.TrimPrefix(line, "LUMEN_DATABASE_URL="), "\"'")
			}
		}
	}
	if dsn == "" {
		t.Fatal("no LUMEN_DATABASE_URL")
	}

	cfg := Config{
		DatabaseURL: dsn,
		Supabase: SupabaseCfg{
			Enabled:             true,
			Interval:            "1h",
			ThreadID:            "smoke-test",
			AppStateTable:       "app_state",
			AppStateDatabaseURL: dsn,
			EgressThreshold:     0.8,
			DBThreshold:         0.8,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	svc, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close()

	if err := svc.CheckSupabaseOnce(ctx); err != nil {
		t.Fatalf("CheckSupabaseOnce: %v", err)
	}
	t.Log("watcher cycle OK (refresh + queries ran, rotated token written back)")
}
