package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// OpenMySQL opens a MySQL connection from a DSN, waits for it to be reachable,
// and applies the schema (idempotent CREATE TABLE IF NOT EXISTS). It returns a
// ready *sql.DB. Callers pass the DSN, e.g.
// "user:pass@tcp(host:3306)/bnbloc?parseTime=true&multiStatements=true".
func OpenMySQL(dsn string) (*sql.DB, error) {
	// multiStatements is required to run the multi-statement schema in one Exec.
	if !strings.Contains(dsn, "multiStatements=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "multiStatements=true"
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)

	// Wait for the database to accept connections (compose start ordering).
	var pingErr error
	for i := range 30 {
		if pingErr = db.Ping(); pingErr == nil {
			break
		}
		if i == 0 {
			log.Printf("bnbloc: store: waiting for mysql at %s to accept connections (retrying for up to 30s): %v", dsnSummary(dsn), pingErr)
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql not reachable: %w", pingErr)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return db, nil
}

// Backends bundles the concrete store implementations a service composes.
// When DB is nil, in-memory implementations are used (local dev / tests).
type Backends struct {
	DB          *sql.DB
	Activity    ActivityStore
	Room        RoomStore
	Grant       GrantStore
	Chat        ChatStore
	Tenant      TenantStore
	Channel     ChannelStore
	BridgeJobs  BridgeJobStore
	Artifacts   ArtifactStore
	Settings    PlatformSettingsStore
	Attachments AttachmentStore
	ExtAudit    ExtensionAuditStore
	AgentPrefs  AgentPrefsStore
	// ComponentInstalls persists per-member opt-in installs of distributed
	// components (rollout=optin extension records).
	ComponentInstalls ComponentInstallStore
	// Prompts is the tenant prompt library (F2).
	Prompts PromptStore
	// MessageTasks holds room messages marked as tasks (F6).
	MessageTasks MessageTaskStore
	// QuotaRequests holds borrowers' requests for more runs on an owner's
	// room authorization (borrow billing, F3).
	QuotaRequests QuotaRequestStore
	// Notifications holds per-member notification prefs and the durable
	// outbound-notification outbox.
	Notifications interface {
		NotificationPrefsStore
		NotificationOutboxStore
	}
	// ReplyTokens holds the reply tokens of delivered notifications
	// (reply from the messenger, N1).
	ReplyTokens NotificationReplyTokenStore
	// WebPush holds members' browser Web Push subscriptions
	// (the built-in push notification channel, N4).
	WebPush WebPushSubscriptionStore
	// RoomImports records the provenance of rooms created by a handoff
	// package import.
	RoomImports RoomImportStore
	// SSOHandoffs holds pending desktop/CLI SSO hand-offs (any gateway
	// replica redeems them).
	SSOHandoffs SSOHandoffStore
	// RefreshTokens tracks refresh-token families (rotation, reuse
	// detection, revocation).
	RefreshTokens RefreshTokenStore
	// APITokens holds super-admin-issued API tokens (O1).
	APITokens APITokenStore
	// Schedules holds rooms' scheduled prompts (N2).
	Schedules RoomScheduleStore
	// Templates is the tenant room-template library (N3).
	Templates RoomTemplateStore
	// Budgets holds the tenants' monthly usage budgets (R12).
	Budgets UsageBudgetStore
	// OrgSync holds organization-sync state (R11): last asserted directory
	// attributes per subject and the default rooms already granted.
	OrgSync OrgSyncStore
	// Profiles holds users' chosen display names (global per user).
	Profiles UserProfileStore
}

// NewBackends builds the store set. If dsn is empty, everything is in-memory.
// Otherwise all stores are MySQL-backed and share one connection pool.
// Either way the choice is LOUD: which storage a service booted with is the
// first thing an operator needs when the stack misbehaves, so it is logged at
// startup — the in-memory fallback is a warning, not a silent dev default.
func NewBackends(dsn string) (*Backends, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		// Production never falls back to in-memory storage: data loss on restart
		// and broken cross-service features must be a boot failure, not a log line.
		if config.Production() {
			return nil, fmt.Errorf("store: BNBLOC_MYSQL_DSN is required when %s=production", config.EnvVar)
		}
		log.Printf("WARNING bnbloc: store: BNBLOC_MYSQL_DSN is not set — running with IN-MEMORY storage. Data does not survive restarts, and cross-service features (bridge job claiming, durable chat history, installed core packages) will NOT work. Set BNBLOC_MYSQL_DSN for a durable deployment.")
		// Room and Channel share the same in-memory storage (Channels() is a
		// view over MemoryRoom's channel table) so the default channel created
		// transactionally with a room is immediately visible to channel reads.
		rooms := NewMemoryRoom()
		return &Backends{
			Activity:          NewMemory(),
			Room:              rooms,
			Grant:             NewMemoryGrant(),
			Chat:              NewMemoryChat(),
			Tenant:            NewMemoryTenant(),
			Channel:           rooms.Channels(),
			BridgeJobs:        NewMemoryBridgeJobs(),
			Artifacts:         NewMemoryArtifacts(),
			Settings:          NewMemoryPlatformSettings(),
			Attachments:       NewMemoryAttachments(),
			ExtAudit:          NewMemoryExtensionAudit(),
			AgentPrefs:        NewMemoryAgentPrefs(),
			ComponentInstalls: NewMemoryComponentInstall(),
			Prompts:           NewMemoryPrompts(),
			MessageTasks:      NewMemoryMessageTasks(),
			QuotaRequests:     NewMemoryQuotaRequests(),
			Notifications:     NewMemoryNotifications(),
			ReplyTokens:       NewMemoryNotificationReplies(),
			WebPush:           NewMemoryWebPush(),
			RoomImports:       NewMemoryRoomImports(),
			SSOHandoffs:       NewMemorySSOHandoff(),
			RefreshTokens:     NewMemoryRefreshToken(),
			APITokens:         NewMemoryAPITokens(),
			Schedules:         NewMemoryRoomSchedules(),
			Templates:         NewMemoryRoomTemplates(),
			Budgets:           NewMemoryUsageBudgets(),
			OrgSync:           NewMemoryOrgSync(),
			Profiles:          NewMemoryUserProfiles(),
		}, nil
	}
	db, err := OpenMySQL(dsn)
	if err != nil {
		return nil, err
	}
	log.Printf("bnbloc: store: mysql backend ready at %s (schema + migrations applied)", dsnSummary(dsn))
	return &Backends{
		DB:                db,
		Activity:          NewMySQL(db),
		Room:              NewMySQLRoom(db),
		Grant:             NewMySQLGrant(db),
		Chat:              NewMySQLChat(db),
		Tenant:            NewMySQLTenant(db),
		Channel:           NewMySQLChannel(db),
		BridgeJobs:        NewMySQLBridgeJobs(db),
		Artifacts:         NewMySQLArtifacts(db),
		Settings:          NewMySQLPlatformSettings(db),
		Attachments:       NewMySQLAttachments(db),
		ExtAudit:          NewMySQLExtensionAudit(db),
		AgentPrefs:        NewMySQLAgentPrefs(db),
		ComponentInstalls: NewMySQLComponentInstall(db),
		Prompts:           NewMySQLPrompts(db),
		MessageTasks:      NewMySQLMessageTasks(db),
		QuotaRequests:     NewMySQLQuotaRequests(db),
		Notifications:     NewMySQLNotifications(db),
		ReplyTokens:       NewMySQLNotificationReplies(db),
		WebPush:           NewMySQLWebPush(db),
		RoomImports:       NewMySQLRoomImports(db),
		SSOHandoffs:       NewMySQLSSOHandoff(db),
		RefreshTokens:     NewMySQLRefreshToken(db),
		APITokens:         NewMySQLAPITokens(db),
		Schedules:         NewMySQLRoomSchedules(db),
		Templates:         NewMySQLRoomTemplates(db),
		Budgets:           NewMySQLUsageBudgets(db),
		OrgSync:           NewMySQLOrgSync(db),
		Profiles:          NewMySQLUserProfiles(db),
	}, nil
}

// HealthCheck returns a readiness checker that pings the database. When DB is
// nil (in-memory dev/test), the checker always reports healthy.
func (b *Backends) HealthCheck() func() error {
	if b == nil || b.DB == nil {
		return func() error { return nil }
	}
	db := b.DB
	return func() error { return db.Ping() }
}

// dsnSummary renders a DSN's address and database for logs, never credentials.
func dsnSummary(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "mysql (unparsed DSN)"
	}
	if cfg.DBName == "" {
		return "mysql://" + cfg.Addr
	}
	return "mysql://" + cfg.Addr + "/" + cfg.DBName
}
