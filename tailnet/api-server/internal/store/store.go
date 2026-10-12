// Package store keeps users, groups, tokens, devices and address books in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Group struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

type User struct {
	ID           int64
	Name         string
	DisplayName  string
	Email        string
	Note         string
	PasswordHash string
	IsAdmin      bool
	Status       int // 1 normal, 0 disabled (same codes as the RustDesk client)
	GroupID      sql.NullInt64
	GroupName    string
	CreatedAt    time.Time
}

type Device struct {
	UUID       string
	RustdeskID string
	Hostname   string
	OS         string
	OSUsername string
	CPU        string
	Memory     string
	Version    string
	UserID     sql.NullInt64
	UserName   string
	GroupName  string
	LastIP     string
	LastSeen   time.Time
	SysinfoAt  sql.NullTime
	CreatedAt  time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS groups (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL UNIQUE,
	display_name  TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	note          TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	status        INTEGER NOT NULL DEFAULT 1,
	group_id      INTEGER REFERENCES groups(id) ON DELETE SET NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
	token_hash  TEXT PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	rustdesk_id TEXT NOT NULL DEFAULT '',
	uuid        TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tokens_user ON tokens(user_id);
CREATE TABLE IF NOT EXISTS devices (
	uuid        TEXT PRIMARY KEY,
	rustdesk_id TEXT NOT NULL,
	hostname    TEXT NOT NULL DEFAULT '',
	os          TEXT NOT NULL DEFAULT '',
	os_username TEXT NOT NULL DEFAULT '',
	cpu         TEXT NOT NULL DEFAULT '',
	memory      TEXT NOT NULL DEFAULT '',
	version     TEXT NOT NULL DEFAULT '',
	user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
	last_ip     TEXT NOT NULL DEFAULT '',
	last_seen   INTEGER NOT NULL,
	sysinfo_at  INTEGER,
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS devices_rustdesk_id ON devices(rustdesk_id);
CREATE INDEX IF NOT EXISTS devices_user ON devices(user_id);
CREATE TABLE IF NOT EXISTS address_books (
	user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	data       TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
`

// Open opens (and creates if needed) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection keeps things simple and
	// avoids SQLITE_BUSY under the light load this server sees.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SetClock overrides the time source (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func fromUnix(v int64) time.Time { return time.Unix(v, 0) }

// ---------- groups ----------

func (s *Store) AddGroup(ctx context.Context, name string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("group name is empty")
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO groups(name, created_at) VALUES(?, ?)`, name, now.Unix())
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Group{ID: id, Name: name, CreatedAt: now}, nil
}

func (s *Store) GroupByName(ctx context.Context, name string) (*Group, error) {
	var g Group
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM groups WHERE name = ?`, name).Scan(&g.ID, &g.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.CreatedAt = fromUnix(created)
	return &g, nil
}

// GroupInfo is a group with its member count.
type GroupInfo struct {
	Group
	Members int
}

func (s *Store) ListGroups(ctx context.Context) ([]GroupInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, g.name, g.created_at, COUNT(u.id)
		FROM groups g LEFT JOIN users u ON u.group_id = g.id
		GROUP BY g.id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupInfo
	for rows.Next() {
		var gi GroupInfo
		var created int64
		if err := rows.Scan(&gi.ID, &gi.Name, &created, &gi.Members); err != nil {
			return nil, err
		}
		gi.CreatedAt = fromUnix(created)
		out = append(out, gi)
	}
	return out, rows.Err()
}

func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- users ----------

const userCols = `u.id, u.name, u.display_name, u.email, u.note, u.password_hash, u.is_admin, u.status, u.group_id, COALESCE(g.name, ''), u.created_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var admin int
	var created int64
	if err := sc.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Email, &u.Note, &u.PasswordHash, &admin, &u.Status, &u.GroupID, &u.GroupName, &created); err != nil {
		return nil, err
	}
	u.IsAdmin = admin != 0
	u.CreatedAt = fromUnix(created)
	return &u, nil
}

// NewUser holds fields for AddUser.
type NewUser struct {
	Name        string
	Password    string
	DisplayName string
	Email       string
	Note        string
	IsAdmin     bool
	Group       string // optional group name; must exist
}

func (s *Store) AddUser(ctx context.Context, nu NewUser) (*User, error) {
	nu.Name = strings.TrimSpace(nu.Name)
	if nu.Name == "" {
		return nil, errors.New("user name is empty")
	}
	if nu.Password == "" {
		return nil, errors.New("password is empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(nu.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	var groupID sql.NullInt64
	if nu.Group != "" {
		g, err := s.GroupByName(ctx, nu.Group)
		if err != nil {
			return nil, fmt.Errorf("group %q: %w", nu.Group, err)
		}
		groupID = sql.NullInt64{Int64: g.ID, Valid: true}
	}
	admin := 0
	if nu.IsAdmin {
		admin = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users(name, display_name, email, note, password_hash, is_admin, status, group_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		nu.Name, nu.DisplayName, nu.Email, nu.Note, string(hash), admin, groupID, s.now().Unix())
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return s.UserByName(ctx, nu.Name)
}

func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u LEFT JOIN groups g ON g.id = u.group_id WHERE u.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u LEFT JOIN groups g ON g.id = u.group_id WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users u LEFT JOIN groups g ON g.id = u.group_id ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword changes the password and revokes all of the user's tokens.
func (s *Store) SetPassword(ctx context.Context, name, password string) error {
	if password == "" {
		return errors.New("password is empty")
	}
	u, err := s.UserByName(ctx, name)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), u.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE user_id = ?`, u.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserGroup assigns a user to a group; group "" removes the assignment.
func (s *Store) SetUserGroup(ctx context.Context, name, group string) error {
	var groupID sql.NullInt64
	if group != "" {
		g, err := s.GroupByName(ctx, group)
		if err != nil {
			return fmt.Errorf("group %q: %w", group, err)
		}
		groupID = sql.NullInt64{Int64: g.ID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET group_id = ? WHERE name = ?`, groupID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserStatus enables (1) or disables (0) a user. Disabling revokes tokens.
func (s *Store) SetUserStatus(ctx context.Context, name string, status int) error {
	u, err := s.UserByName(ctx, name)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET status = ? WHERE id = ?`, status, u.ID); err != nil {
		return err
	}
	if status != 1 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM tokens WHERE user_id = ?`, u.ID)
	}
	return err
}

// Authenticate checks name/password. It returns ErrNotFound for unknown users,
// wrong passwords and disabled users alike so callers cannot tell them apart.
func (s *Store) Authenticate(ctx context.Context, name, password string) (*User, error) {
	u, err := s.UserByName(ctx, name)
	if errors.Is(err, ErrNotFound) {
		// Spend comparable time to avoid a user-enumeration timing signal.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || u.Status != 1 {
		return nil, ErrNotFound
	}
	return u, nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)

// GroupMembers returns enabled users visible to u: everyone in u's group, or
// just u if u has no group. Ordered by name.
func (s *Store) GroupMembers(ctx context.Context, u *User) ([]User, error) {
	var rows *sql.Rows
	var err error
	if u.GroupID.Valid {
		rows, err = s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users u LEFT JOIN groups g ON g.id = u.group_id
			WHERE u.group_id = ? AND u.status = 1 ORDER BY u.name`, u.GroupID.Int64)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users u LEFT JOIN groups g ON g.id = u.group_id
			WHERE u.id = ? ORDER BY u.name`, u.ID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		m, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ---------- tokens ----------

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// IssueToken creates a new bearer token for user. Only its SHA-256 is stored.
func (s *Store) IssueToken(ctx context.Context, userID int64, rustdeskID, uuid string, ttl time.Duration) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	now := s.now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(token_hash, user_id, rustdesk_id, uuid, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?)`,
		hashToken(tok), userID, rustdeskID, uuid, now.Unix(), now.Add(ttl).Unix())
	if err != nil {
		return "", err
	}
	// Opportunistic cleanup of expired tokens.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM tokens WHERE expires_at <= ?`, now.Unix())
	return tok, nil
}

// UserByToken resolves a bearer token to an enabled user. When the token is
// past half its lifetime it is extended by ttl (sliding expiry) so devices that
// stay in use do not get logged out.
func (s *Store) UserByToken(ctx context.Context, tok string, ttl time.Duration) (*User, error) {
	if tok == "" {
		return nil, ErrNotFound
	}
	h := hashToken(tok)
	var userID, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires_at FROM tokens WHERE token_hash = ?`, h).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if expires <= now.Unix() {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tokens WHERE token_hash = ?`, h)
		return nil, ErrNotFound
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Status != 1 {
		return nil, ErrNotFound
	}
	if time.Unix(expires, 0).Sub(now) < ttl/2 {
		_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET expires_at = ? WHERE token_hash = ?`, now.Add(ttl).Unix(), h)
	}
	return u, nil
}

func (s *Store) RevokeToken(ctx context.Context, tok string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE token_hash = ?`, hashToken(tok))
	return err
}

// ---------- devices ----------

// BindDevice records that the device (rustdeskID, uuid) is logged in as user.
// Only called from authenticated endpoints. Stale rows that claim the same
// RustDesk ID with a different uuid are removed.
func (s *Store) BindDevice(ctx context.Context, rustdeskID, uuid string, userID int64, ip string) error {
	if rustdeskID == "" || uuid == "" {
		return nil
	}
	now := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE rustdesk_id = ? AND uuid <> ?`, rustdeskID, uuid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO devices(uuid, rustdesk_id, user_id, last_ip, last_seen, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET rustdesk_id = excluded.rustdesk_id, user_id = excluded.user_id,
			last_ip = excluded.last_ip, last_seen = excluded.last_seen`,
		uuid, rustdeskID, userID, ip, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// UnbindDevice clears the user of the device, if it is bound to userID.
func (s *Store) UnbindDevice(ctx context.Context, uuid string, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET user_id = NULL WHERE uuid = ? AND user_id = ?`, uuid, userID)
	return err
}

// Heartbeat updates last-seen/IP for a device, creating a bare row if needed.
// It reports whether the server still needs the device's sysinfo.
func (s *Store) Heartbeat(ctx context.Context, rustdeskID, uuid, ip string) (needSysinfo, bound bool, err error) {
	now := s.now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO devices(uuid, rustdesk_id, last_ip, last_seen, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET rustdesk_id = excluded.rustdesk_id, last_ip = excluded.last_ip, last_seen = excluded.last_seen`,
		uuid, rustdeskID, ip, now, now)
	if err != nil {
		return false, false, err
	}
	var sysinfoAt, userID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT sysinfo_at, user_id FROM devices WHERE uuid = ?`, uuid).Scan(&sysinfoAt, &userID); err != nil {
		return false, false, err
	}
	return !sysinfoAt.Valid, userID.Valid, nil
}

// Sysinfo holds the fields of the client's /api/sysinfo body that we keep.
type Sysinfo struct {
	RustdeskID string
	UUID       string
	Hostname   string
	OS         string
	OSUsername string
	CPU        string
	Memory     string
	Version    string
}

func (s *Store) UpdateSysinfo(ctx context.Context, si Sysinfo, ip string) error {
	now := s.now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO devices(uuid, rustdesk_id, hostname, os, os_username, cpu, memory, version, last_ip, last_seen, sysinfo_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET rustdesk_id = excluded.rustdesk_id, hostname = excluded.hostname, os = excluded.os,
			os_username = excluded.os_username, cpu = excluded.cpu, memory = excluded.memory, version = excluded.version,
			last_ip = excluded.last_ip, last_seen = excluded.last_seen, sysinfo_at = excluded.sysinfo_at`,
		si.UUID, si.RustdeskID, si.Hostname, si.OS, si.OSUsername, si.CPU, si.Memory, si.Version, ip, now, now, now)
	return err
}

const deviceCols = `d.uuid, d.rustdesk_id, d.hostname, d.os, d.os_username, d.cpu, d.memory, d.version, d.user_id,
	COALESCE(u.name, ''), COALESCE(g.name, ''), d.last_ip, d.last_seen, d.sysinfo_at, d.created_at`

func scanDevice(sc interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var lastSeen, created int64
	var sysinfoAt sql.NullInt64
	if err := sc.Scan(&d.UUID, &d.RustdeskID, &d.Hostname, &d.OS, &d.OSUsername, &d.CPU, &d.Memory, &d.Version, &d.UserID,
		&d.UserName, &d.GroupName, &d.LastIP, &lastSeen, &sysinfoAt, &created); err != nil {
		return nil, err
	}
	d.LastSeen = fromUnix(lastSeen)
	d.CreatedAt = fromUnix(created)
	if sysinfoAt.Valid {
		d.SysinfoAt = sql.NullTime{Time: fromUnix(sysinfoAt.Int64), Valid: true}
	}
	return &d, nil
}

func (s *Store) queryDevices(ctx context.Context, where string, args ...any) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices d
		LEFT JOIN users u ON u.id = d.user_id LEFT JOIN groups g ON g.id = u.group_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ListDevices returns all devices (admin CLI).
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	return s.queryDevices(ctx, `ORDER BY COALESCE(u.name, '~'), d.rustdesk_id`)
}

// AccessibleDevices returns devices logged in by users visible to u
// (same group, enabled), or u's own devices if u has no group.
func (s *Store) AccessibleDevices(ctx context.Context, u *User) ([]Device, error) {
	if u.GroupID.Valid {
		return s.queryDevices(ctx, `WHERE u.group_id = ? AND u.status = 1 ORDER BY d.rustdesk_id`, u.GroupID.Int64)
	}
	return s.queryDevices(ctx, `WHERE d.user_id = ? ORDER BY d.rustdesk_id`, u.ID)
}

func (s *Store) DeleteDevice(ctx context.Context, rustdeskID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE rustdesk_id = ?`, rustdeskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- address book (legacy single-blob format) ----------

// AddressBook returns the user's address book JSON string ("" if none).
func (s *Store) AddressBook(ctx context.Context, userID int64) (string, time.Time, error) {
	var data string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT data, updated_at FROM address_books WHERE user_id = ?`, userID).Scan(&data, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return data, fromUnix(updated), err
}

func (s *Store) SetAddressBook(ctx context.Context, userID int64, data string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO address_books(user_id, data, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		userID, data, s.now().Unix())
	return err
}
