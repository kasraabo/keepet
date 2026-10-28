package app

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	assets "keepet"
)

// Exercise upgrades with historical data, not just migrations on an empty database.
func TestReleaseUpgradeFromVersion11(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("isolated test database required")
	}
	ctx := context.Background()
	control, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	name := "keepet_upgrade_" + token()[:12] + "_test"
	if _, e = control.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := control.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	}()
	u.Path = "/" + name
	child := u.String()
	source, e := iofs.New(assets.Files, "migrations")
	if e != nil {
		t.Fatal(e)
	}
	m, e := migrate.NewWithSourceInstance("iofs", source, child)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Migrate(11); e != nil {
		t.Fatal(e)
	}
	sErr, dErr := m.Close()
	if sErr != nil || dErr != nil {
		t.Fatal(sErr, dErr)
	}
	db, e := pgxpool.New(ctx, child)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", child)
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	originalKey := filepath.Join(t.TempDir(), "original.key")
	t.Setenv("SETTINGS_KEY_FILE", originalKey)
	cipher, e := sealIntegration("sms", "local-upgrade-test-secret")
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE platform_integrations SET secret=$1,revision=17 WHERE id='sms'", cipher)
	exec(`INSERT INTO users(id,phone,name,role) VALUES('upgrade-owner','09127771110','نام تاریخی مشتری','owner'),('upgrade-provider','09127771111','نام تاریخی متخصص','provider')`)
	exec(`INSERT INTO providers(id,user_id,name,service,bio,price,verified,active) VALUES('upgrade-vet','upgrade-provider','پزشک تاریخی','vet','معرفی محفوظ',350000,true,true)`)
	exec(`INSERT INTO sessions(token,user_id,csrf,expires_at) VALUES('upgrade-session','upgrade-owner','old-csrf',now()+interval '1 day')`)
	exec(`INSERT INTO pets(id,user_id,name,species,age) VALUES('upgrade-pet','upgrade-owner','پت تاریخی','cat','۲ سال')`)
	exec(`INSERT INTO bookings(id,session_token,owner_id,service,provider_id,pet_name,payment_key,payment_status,status,amount,provider_due,prescription) VALUES('upgrade-booking','upgrade-session','upgrade-owner','vet','upgrade-vet','پت تاریخی','upgrade-payment','paid','completed',500000,350000,'تجویز تاریخی محفوظ')`)
	// Credited before migration 12: a repeated completion update must not pay again.
	exec(`SELECT wallet_post('upgrade-provider','service_income',350000,0,0,'income:upgrade-booking','upgrade-booking',NULL,'درآمد قبلی')`)
	exec(`SELECT wallet_post('upgrade-owner','admin_cash',900000,0,0,'upgrade-cash',NULL,NULL,'موجودی قبلی')`)
	exec(`SELECT wallet_post('upgrade-owner','admin_bonus',0,200000,0,'upgrade-bonus',NULL,NULL,'هدیه قبلی')`)
	exec(`INSERT INTO wallet_bank_accounts(id,user_id,iban,holder,status) VALUES('upgrade-bank','upgrade-owner','IR062960000000100324200001','نام تاریخی مشتری','verified')`)
	exec(`INSERT INTO wallet_withdrawals(id,user_id,amount,bank_id,bank_revision,iban,holder,request_key) VALUES('upgrade-withdrawal','upgrade-owner',100000,'upgrade-bank',1,'IR062960000000100324200001','نام تاریخی مشتری','upgrade-withdrawal-key')`)
	exec(`SELECT wallet_post('upgrade-owner','withdrawal_hold',-100000,0,100000,'hold:upgrade-withdrawal',NULL,'upgrade-owner','برداشت قبلی')`)
	exec(`INSERT INTO tickets(id,user_id,subject,body,email) VALUES('upgrade-ticket','upgrade-owner','تیکت تاریخی','متن تاریخی محفوظ','old@example.test')`)
	exec(`INSERT INTO business_config(kind,id,data,revision) VALUES('article','upgrade-article','{"name":"مقالهٔ قبلی","body":"متن قبلی","enabled":true}',9)`)
	exec(`UPDATE referral_settings SET invitee_bonus=123000,vet_bps=750,enabled=false WHERE id=1`)
	db.Close()
	// Simulate moving the matching key backup to the new installation.
	restoredKey := filepath.Join(t.TempDir(), "restored.key")
	raw, e := os.ReadFile(originalKey)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(restoredKey, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SETTINGS_KEY_FILE", restoredKey)
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := a.DB.QueryRow(ctx, q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	checks := []struct {
		q    string
		want int64
	}{
		{"SELECT version FROM schema_migrations WHERE NOT dirty", 21},
		{"SELECT count(*) FROM users WHERE id='upgrade-owner' AND name='نام تاریخی مشتری' AND role='owner'", 1},
		{"SELECT count(*) FROM bookings WHERE id='upgrade-booking' AND NOT request_flow AND NOT time_flow AND NOT fulfillment_flow AND status='completed' AND payment_status='paid' AND prescription='تجویز تاریخی محفوظ'", 1},
		{"SELECT count(*) FROM pets WHERE id='upgrade-pet' AND name='پت تاریخی'", 1},
		{"SELECT cash FROM wallets WHERE user_id='upgrade-owner'", 800000},
		{"SELECT bonus FROM wallets WHERE user_id='upgrade-owner'", 200000},
		{"SELECT reserved FROM wallets WHERE user_id='upgrade-owner'", 100000},
		{"SELECT cash FROM wallets WHERE user_id='upgrade-provider'", 350000},
		{"SELECT count(*) FROM wallet_withdrawals WHERE id='upgrade-withdrawal' AND status='pending' AND amount=100000", 1},
		{"SELECT count(*) FROM wallet_bank_accounts WHERE id='upgrade-bank' AND status='verified'", 1},
		{"SELECT count(*) FROM tickets WHERE id='upgrade-ticket' AND body='متن تاریخی محفوظ'", 1},
		{"SELECT count(*) FROM business_config WHERE kind='article' AND id='upgrade-article' AND revision=9 AND data->>'body'='متن قبلی'", 1},
		{"SELECT count(*) FROM referral_settings WHERE invitee_bonus=123000 AND vet_bps=750 AND NOT enabled", 1},
		{"SELECT count(*) FROM notifications WHERE source_user_id IN ('upgrade-owner','upgrade-provider')", 0},
	}
	for _, c := range checks {
		if got := scalar(c.q); got != c.want {
			t.Fatalf("%s got %d want %d", c.q, got, c.want)
		}
	}
	var saved []byte
	if e = a.DB.QueryRow(ctx, "SELECT secret FROM platform_integrations WHERE id='sms'").Scan(&saved); e != nil {
		t.Fatal(e)
	}
	if secret, e := openIntegration("sms", saved); e != nil || secret != "local-upgrade-test-secret" {
		t.Fatal("restored key cannot decrypt historical secret", e)
	}
	if _, e = a.DB.Exec(ctx, "UPDATE bookings SET status='completed' WHERE id='upgrade-booking'"); e != nil {
		t.Fatal(e)
	}
	if scalar("SELECT cash FROM wallets WHERE user_id='upgrade-provider'") != 350000 || scalar("SELECT count(*) FROM wallet_entries WHERE idempotency_key='income:upgrade-booking'") != 1 {
		t.Fatal("historical settlement paid twice")
	}
	a.Close()
	again, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	var cash int64
	if e = again.DB.QueryRow(ctx, "SELECT cash FROM wallets WHERE user_id='upgrade-provider'").Scan(&cash); e != nil || cash != 350000 {
		t.Fatal("restart changed historical balance", e)
	}
}
