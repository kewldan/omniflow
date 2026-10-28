package config

import "testing"

func setWorkerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_DATABASE_URL", "postgres://omniflow@localhost/omniflow")
	t.Setenv("APP_REMNAWAVE_URL", "https://panel.example.test")
	t.Setenv("APP_REMNAWAVE_TOKEN", "token")
}

// Each process gets its own pool size by default, because their concurrency
// differs by an order of magnitude and pgxpool's own default fits none of them.
func TestEachProcessHasItsOwnDefaultPoolSize(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("APP_TELEMETRY_ENABLED", "false")
	t.Setenv("APP_VALKEY_URL", "redis://localhost:6379")
	t.Setenv("APP_TELEGRAM_TOKEN", "1:token")

	api, err := Load()
	if err != nil {
		t.Fatalf("load api: %v", err)
	}
	bot, err := LoadBot()
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	worker, err := LoadWorker()
	if err != nil {
		t.Fatalf("load worker: %v", err)
	}
	if api.DatabaseMaxConns != DefaultAPIDatabaseMaxConns ||
		bot.DatabaseMaxConns != DefaultBotDatabaseMaxConns ||
		worker.DatabaseMaxConns != DefaultWorkerDatabaseMaxConns {
		t.Fatalf("pool sizes = %d/%d/%d", api.DatabaseMaxConns, bot.DatabaseMaxConns, worker.DatabaseMaxConns)
	}

	t.Setenv("APP_DATABASE_MAX_CONNS", "64")
	if worker, err = LoadWorker(); err != nil || worker.DatabaseMaxConns != 64 {
		t.Fatalf("an explicit size was not applied: %d, %v", worker.DatabaseMaxConns, err)
	}
}

// A pool size the process cannot use is refused rather than silently replaced
// by the default the operator was trying to change.
func TestAnInvalidPoolSizeIsRefused(t *testing.T) {
	setWorkerEnvironment(t)
	for _, value := range []string{"0", "-3", "many", "99999999999"} {
		t.Setenv("APP_DATABASE_MAX_CONNS", value)
		if _, err := LoadWorker(); err == nil {
			t.Fatalf("APP_DATABASE_MAX_CONNS=%s was accepted", value)
		}
	}
}

// The worker settles orders too, so it must enforce the same wallet policy the
// api and the bot read from the same variables. It used to carry none.
func TestTheWorkerEnforcesTheSameTopUpPolicy(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("APP_WALLET_TOPUP_MAXIMUM_MINOR", "777000")
	worker, err := LoadWorker()
	if err != nil {
		t.Fatalf("load worker: %v", err)
	}
	limits := worker.Commerce().TopUp.Limits()
	if !limits.Enabled || limits.MaximumMinor != 777000 || len(limits.Presets) == 0 {
		t.Fatalf("worker top-up limits = %+v", limits)
	}

	t.Setenv("APP_WALLET_TOPUP_MINIMUM_MINOR", "900000")
	if _, err = LoadWorker(); err == nil {
		t.Fatal("the worker accepted a top-up minimum above the maximum")
	}
}
