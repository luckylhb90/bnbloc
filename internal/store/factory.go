package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// Connection pool sizing. Every service process opens one pool shared by all
// of its stores. maxOpenConns bounds what one process may hold against the
// server's max_connections (151 by default, shared by every service and
// replica). The pool keeps as many idle connections as it may open: with a
// smaller idle cap, every burst above it closes the surplus on release and
// dials it again on the next burst (a 30 s load run at 50 msgs/s opened 165
// connections that way). connMaxLifetime still recycles connections
// steadily, so a failover or a load balancer rebalance is picked up.
const (
	maxOpenConns    = 20
	connMaxLifetime = 3 * time.Minute
)

// OpenMySQL opens a MySQL connection from a DSN, waits for it to be reachable,
// and applies the schema (idempotent CREATE TABLE IF NOT EXISTS). It returns a
// ready *sql.DB. Callers pass the DSN, e.g.
// "user:pass@tcp(host:3306)/bnbloc?parseTime=true"; mysqlConfig adds the
// client settings the stores rely on.
func OpenMySQL(dsn string) (*sql.DB, error) {
	cfg, err := mysqlConfig(dsn)
	if err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)

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
	if err := requireUTF8MB4Session(db); err != nil {
		_ = db.Close()
		return nil, err
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

// mysqlConfig parses the operator's DSN and adds the client settings the
// stores rely on:
//
//   - multiStatements, to apply the multi-statement schema in one Exec;
//   - interpolateParams, so a parameterized query is one round trip (the
//     driver escapes the arguments into the query text) instead of two
//     (server-side Prepare, then Execute) plus a Close packet. Client-side
//     escaping is only
//     safe in a connection charset whose multibyte sequences never contain
//     the byte 0x5c (backslash): the driver documents BIG5, CP932, GB2312,
//     GBK and SJIS as unsafe, and its own check covers only the collation
//     parameter. The schema is utf8mb4 throughout, so the connection must be
//     utf8mb4 as well: a DSN that asks for any other charset or collation, or
//     sets a character_set_* session variable, is refused here, and the
//     negotiated session charset is verified once connected
//     (requireUTF8MB4Session). The driver's default is the utf8mb4_general_ci
//     handshake collation.
func mysqlConfig(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("parse mysql dsn: %w", err)
	}
	if c := cfg.Collation; c != "" && !strings.HasPrefix(strings.ToLower(c), "utf8mb4_") {
		return nil, fmt.Errorf("mysql dsn: collation %q is not utf8mb4 (the schema is utf8mb4; remove the collation parameter)", c)
	}
	for k, v := range cfg.Params {
		switch k := strings.ToLower(k); {
		case k == "charset":
			if !strings.EqualFold(strings.TrimSpace(v), "utf8mb4") {
				return nil, fmt.Errorf("mysql dsn: charset %q is not utf8mb4 (the schema is utf8mb4; remove the charset parameter)", v)
			}
		case strings.HasPrefix(k, "character_set_") || k == "names":
			return nil, fmt.Errorf("mysql dsn: the %s session variable is managed by the store (utf8mb4); remove it", k)
		}
	}
	cfg.MultiStatements = true
	cfg.InterpolateParams = true
	return cfg, nil
}

// jsonArg marshals v for a JSON column argument. JSON text must reach the
// driver as json.RawMessage (or string), never as []byte: with
// interpolateParams the driver writes a []byte argument as a _binary'…'
// literal, which MySQL refuses for a JSON column (error 3144) and compares
// byte-wise against a text column. json.RawMessage is written as a quoted
// utf8mb4 string, exactly what the prepared-statement protocol sent before.
// Every JSON column write goes through this helper (or passes a stored
// json.RawMessage unchanged).
func jsonArg(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// requireUTF8MB4Session verifies the charset the server actually negotiated
// (the handshake collation can be overridden server-side, e.g. by
// init_connect or skip-character-set-client-handshake): parameter
// interpolation is only enabled on a utf8mb4 session.
func requireUTF8MB4Session(db *sql.DB) error {
	var client, conn string
	if err := db.QueryRow(`SELECT @@character_set_client, @@character_set_connection`).Scan(&client, &conn); err != nil {
		return fmt.Errorf("read session charset: %w", err)
	}
	if client != "utf8mb4" || conn != "utf8mb4" {
		return fmt.Errorf("mysql session charset is client=%s connection=%s, want utf8mb4 (check the server's init_connect and character-set-client-handshake settings)", client, conn)
	}
	return nil
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
	// ContentComponents holds content components (experts, skill packs),
	// their versions, uses and permission acks.
	ContentComponents ContentComponentStore
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
			ContentComponents: NewMemoryContentComponents(),
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
		ContentComponents: NewMySQLContentComponents(db),
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
