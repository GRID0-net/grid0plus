package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the production Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// Ping reports whether the pool can reach PostgreSQL.
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// OpenPostgres connects to PostgreSQL and, if autoMigrate is set, brings the
// schema up to date.
func OpenPostgres(ctx context.Context, url string, maxConns int32, autoMigrate bool) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if autoMigrate {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("acquire connection for migration: %w", err)
		}
		_, err = Migrate(ctx, conn.Conn())
		conn.Release()
		if err != nil {
			pool.Close()
			return nil, err
		}
	}

	return &Postgres{pool: pool}, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

// Pool exposes the underlying pool, for the dashboard's read-only queries.
func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }

// mapErr translates pgx errors into the sentinels handlers switch on.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrNotFound, pgErr.ConstraintName)
		case "23514": // check_violation
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		}
	}
	return err
}

// ---- users ---------------------------------------------------------------

const userColumns = `
	id, nickname, country, birthday,
	thumbnail_url, thumbnail2_url, thumbnail_uploaded_at,
	permissions, extras, deleted,
	blocks_updated_at, friends_updated_at, created_at, updated_at,
	login_username, login_password_hash, provisional`

// withDiscordOwner appends the Discord account that owns the user to a users
// column list, read from bot_account_links. It is a joined expression rather
// than a plain column so that prefixColumns, which FindUsers uses to qualify
// the list with its table alias, can keep treating userColumns as a bare list
// of columns. prefix is how the caller refers to the users table.
func withDiscordOwner(cols, prefix string) string {
	return cols + `, (SELECT owner.discord_user_id FROM bot_account_links owner WHERE owner.user_id = ` + prefix + `id)`
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	var perms, extras []byte
	var loginUsername, loginPasswordHash, discordUserID *string
	err := row.Scan(
		&u.ID, &u.Nickname, &u.Country, &u.Birthday,
		&u.ThumbnailURL, &u.Thumbnail2URL, &u.ThumbnailUploadedAt,
		&perms, &extras, &u.Deleted,
		&u.BlocksUpdatedAt, &u.FriendsUpdatedAt, &u.CreatedAt, &u.UpdatedAt,
		&loginUsername, &loginPasswordHash, &u.Provisional, &discordUserID,
	)
	if err != nil {
		return User{}, mapErr(err)
	}
	if loginUsername != nil {
		u.LoginUsername = *loginUsername
	}
	if loginPasswordHash != nil {
		u.LoginPasswordHash = *loginPasswordHash
	}
	if discordUserID != nil {
		u.DiscordUserID = *discordUserID
	}
	if err := json.Unmarshal(perms, &u.Permissions); err != nil {
		return User{}, fmt.Errorf("decode permissions for %s: %w", u.ID, err)
	}
	if err := json.Unmarshal(extras, &u.Extras); err != nil {
		return User{}, fmt.Errorf("decode extras for %s: %w", u.ID, err)
	}
	if u.Extras == nil {
		u.Extras = EmptyExtras()
	}
	return u, nil
}

// CreateUser inserts a user and its first device account in one transaction.
func (p *Postgres) CreateUser(ctx context.Context, u User, da DeviceAccount) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	extras, err := json.Marshal(u.Extras)
	if err != nil {
		return err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO users (
			id, nickname, country, birthday,
			thumbnail_url, thumbnail2_url, thumbnail_uploaded_at,
			permissions, extras,
			blocks_updated_at, friends_updated_at, created_at, updated_at, provisional)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$10,$10,$11)`,
		u.ID, u.Nickname, u.Country, u.Birthday,
		u.ThumbnailURL, u.Thumbnail2URL, u.ThumbnailUploadedAt,
		perms, extras, u.CreatedAt, u.Provisional)
	if err != nil {
		return mapErr(err)
	}

	var deviceID any
	if da.DeviceID != "" {
		deviceID = da.DeviceID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO device_accounts (id, user_id, password_sha256, device_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5)`,
		da.ID, u.ID, da.PasswordSHA256, deviceID, da.CreatedAt)
	if err != nil {
		return mapErr(err)
	}

	// A presence row from the start, so every read path can assume it exists.
	// reported_at is left NULL: the console has not reported anything yet.
	// Setting it to now() here would make a brand new account look like it had
	// just reported OFFLINE, which suppresses the liveness rule.
	_, err = tx.Exec(ctx, `
		INSERT INTO presence (user_id, state, extras, last_seen_at)
		VALUES ($1, $2, $3, $4)`,
		u.ID, StateOffline, []byte(`{}`), u.CreatedAt)
	if err != nil {
		return mapErr(err)
	}

	return tx.Commit(ctx)
}

// CreateUserWithLogin inserts a user with login credentials and no device
// account, and links it to the Discord account in u.DiscordUserID.
func (p *Postgres) CreateUserWithLogin(ctx context.Context, u User, primary bool) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	extras, err := json.Marshal(u.Extras)
	if err != nil {
		return err
	}

	// The user, its presence row and its Discord link go in together: an
	// account nobody owns is an orphan the bot cannot reach again, and
	// half of one is worse than none.
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO users (
			id, nickname, country, birthday,
			thumbnail_url, thumbnail2_url, thumbnail_uploaded_at,
			permissions, extras,
			blocks_updated_at, friends_updated_at, created_at, updated_at,
			login_username, login_password_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$10,$10,$11,$12)`,
		u.ID, u.Nickname, u.Country, u.Birthday,
		u.ThumbnailURL, u.Thumbnail2URL, u.ThumbnailUploadedAt,
		perms, extras, u.CreatedAt,
		u.LoginUsername, u.LoginPasswordHash)
	if err != nil {
		return mapErr(err)
	}

	// A presence row from the start, matching CreateUser, so every read path
	// can assume it exists even before this user ever links a console.
	if _, err := tx.Exec(ctx, `
		INSERT INTO presence (user_id, state, extras, last_seen_at)
		VALUES ($1, $2, $3, $4)`,
		u.ID, StateOffline, []byte(`{}`), u.CreatedAt); err != nil {
		return mapErr(err)
	}

	if u.DiscordUserID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO bot_account_links (user_id, discord_user_id, primary_account)
			VALUES ($1, $2, $3)`,
			u.ID, u.DiscordUserID, primary); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit(ctx)
}

// CreateDeviceAccountForUser adds a device account to an existing user.
func (p *Postgres) CreateDeviceAccountForUser(ctx context.Context, da DeviceAccount) error {
	var deviceID any
	if da.DeviceID != "" {
		deviceID = da.DeviceID
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO device_accounts (id, user_id, password_sha256, device_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5)`,
		da.ID, da.UserID, da.PasswordSHA256, deviceID, da.CreatedAt)
	return mapErr(err)
}

// GetUserByLoginUsername looks up a user by their login username.
func (p *Postgres) GetUserByLoginUsername(ctx context.Context, username string) (User, error) {
	return scanUser(p.pool.QueryRow(ctx,
		`SELECT `+withDiscordOwner(userColumns, "users.")+
			` FROM users WHERE lower(login_username) = lower($1)`, username))
}

// GetUserByDiscordID returns the main account of the accounts a Discord
// account owns.
func (p *Postgres) GetUserByDiscordID(ctx context.Context, discordUserID string) (User, error) {
	if discordUserID == "" {
		return User{}, ErrNotFound
	}
	return scanUser(p.pool.QueryRow(ctx,
		`SELECT `+withDiscordOwner(userColumns, "users.")+`
		 FROM users JOIN bot_account_links bl ON bl.user_id = users.id
		 WHERE bl.discord_user_id = $1 AND bl.primary_account`, discordUserID))
}

// ListUsersByDiscordID returns every account a Discord account owns, its main
// account first, then oldest.
func (p *Postgres) ListUsersByDiscordID(ctx context.Context, discordUserID string) ([]User, error) {
	if discordUserID == "" {
		return nil, nil
	}
	return p.queryUsers(ctx,
		`SELECT `+withDiscordOwner(userColumns, "users.")+`
		 FROM users JOIN bot_account_links bl ON bl.user_id = users.id
		 WHERE bl.discord_user_id = $1
		 ORDER BY bl.primary_account DESC, users.created_at`, discordUserID)
}

// GetUserByDiscordLogin returns one of the accounts a Discord account owns,
// named by its login. An account belonging to somebody else is not an account
// this Discord account owns, so the join does the refusing.
func (p *Postgres) GetUserByDiscordLogin(ctx context.Context, discordUserID, username string) (User, error) {
	if discordUserID == "" || username == "" {
		return User{}, ErrNotFound
	}
	return scanUser(p.pool.QueryRow(ctx,
		`SELECT `+withDiscordOwner(userColumns, "users.")+`
		 FROM users JOIN bot_account_links bl ON bl.user_id = users.id
		 WHERE bl.discord_user_id = $1 AND lower(users.login_username) = lower($2)`,
		discordUserID, username))
}

// LinkBotAccount attaches an account to the Discord account that owns it.
// Linking the same pair again is a no-op rather than an error, so running
// /link twice does not fail -- and it can promote an account to main but never
// demote one, which is why the main flag is OR-ed rather than assigned. An
// account another Discord account owns is a conflict: the ON CONFLICT clause
// below does not match, and nothing is written. A second main account for a
// Discord account that has one is a conflict from the partial unique index.
func (p *Postgres) LinkBotAccount(ctx context.Context, discordUserID, userID string, primary bool) error {
	if discordUserID == "" || userID == "" {
		return fmt.Errorf("%w: a link needs a Discord account and a user", ErrNotFound)
	}
	// Whether the user exists is answered first, on purpose. Postgres checks
	// the indexes in its own order, so a link for a user that does not exist
	// would otherwise be refused as a duplicate main account whenever the
	// caller already had one -- a different, and wrong, reason for the same
	// "no".
	if _, err := p.GetUser(ctx, userID); err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO bot_account_links (user_id, discord_user_id, primary_account)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		   SET discord_user_id = EXCLUDED.discord_user_id,
		       primary_account  = bot_account_links.primary_account OR EXCLUDED.primary_account
		 WHERE bot_account_links.discord_user_id = EXCLUDED.discord_user_id`,
		userID, discordUserID, primary)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: user %s belongs to another Discord account", ErrConflict, userID)
	}
	return nil
}

// GetUser returns a user by id.
func (p *Postgres) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(p.pool.QueryRow(ctx,
		`SELECT `+withDiscordOwner(userColumns, "users.")+` FROM users WHERE users.id = $1`, id))
}

// UpdateUser applies a patch and returns the updated user.
func (p *Postgres) UpdateUser(ctx context.Context, id string, patch UserPatch) (User, error) {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if patch.Nickname != nil {
		add("nickname", *patch.Nickname)
	}
	if patch.Country != nil {
		add("country", *patch.Country)
	}
	if patch.Birthday != nil {
		add("birthday", *patch.Birthday)
	}
	if patch.ThumbnailURL != nil {
		add("thumbnail_url", *patch.ThumbnailURL)
	}
	if patch.Thumbnail2URL != nil {
		add("thumbnail2_url", *patch.Thumbnail2URL)
	}
	if patch.ThumbnailUploadedAt != nil {
		add("thumbnail_uploaded_at", *patch.ThumbnailUploadedAt)
	}
	if patch.Permissions != nil {
		buf, err := json.Marshal(*patch.Permissions)
		if err != nil {
			return User{}, err
		}
		add("permissions", buf)
	}
	if patch.Extras != nil {
		buf, err := json.Marshal(*patch.Extras)
		if err != nil {
			return User{}, err
		}
		add("extras", buf)
	}
	if patch.Deleted != nil {
		add("deleted", *patch.Deleted)
	}
	if patch.LoginPasswordHash != nil {
		add("login_password_hash", *patch.LoginPasswordHash)
	}

	q := `UPDATE users SET ` + strings.Join(sets, ", ") +
		` WHERE id = $1 RETURNING ` + withDiscordOwner(userColumns, "users.")
	return scanUser(p.pool.QueryRow(ctx, q, args...))
}

// FindUsers returns the users matching a query.
func (p *Postgres) FindUsers(ctx context.Context, q UserQuery) ([]User, error) {
	where := []string{"NOT u.deleted"}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	join := ""
	if q.FriendCode != "" {
		join = " JOIN friend_codes fc ON fc.user_id = u.id"
		add("fc.code = $%d", q.FriendCode)
	}
	if q.Nickname != "" {
		add("lower(u.nickname) = lower($%d)", q.Nickname)
	}
	if q.IDs != nil {
		// An empty array matches nothing, which is what a non-nil empty IDs
		// slice is documented to mean.
		add("u.id = ANY($%d)", q.IDs)
	}

	sql := `SELECT ` + withDiscordOwner(prefixColumns(userColumns, "u."), "u.") +
		` FROM users u` + join +
		` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY u.created_at DESC`
	if q.Limit > 0 {
		args = append(args, q.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return p.queryUsers(ctx, sql, args...)
}

// ListUsers returns every user, newest first.
func (p *Postgres) ListUsers(ctx context.Context, limit int) ([]User, error) {
	sql := `SELECT ` + withDiscordOwner(userColumns, "users.") +
		` FROM users WHERE NOT deleted ORDER BY created_at DESC`
	args := []any{}
	if limit > 0 {
		args = append(args, limit)
		sql += " LIMIT $1"
	}
	return p.queryUsers(ctx, sql, args...)
}

func (p *Postgres) queryUsers(ctx context.Context, sql string, args ...any) ([]User, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, mapErr(rows.Err())
}

// prefixColumns qualifies a bare column list with a table alias.
func prefixColumns(cols, prefix string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// ---- devices and device accounts ----------------------------------------

// UpsertDevice records that a console was seen.
func (p *Postgres) UpsertDevice(ctx context.Context, d Device) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO devices (id, serial_number, product_code, device_type, ist, system_version,
		                     first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (id) DO UPDATE SET
			serial_number  = EXCLUDED.serial_number,
			product_code   = EXCLUDED.product_code,
			device_type    = EXCLUDED.device_type,
			ist            = EXCLUDED.ist,
			system_version = EXCLUDED.system_version,
			last_seen_at   = EXCLUDED.last_seen_at`,
		d.ID, d.SerialNumber, d.ProductCode, d.DeviceType, d.IsT, d.SystemVersion, d.LastSeenAt)
	return mapErr(err)
}

const deviceAccountColumns = `id, user_id, password_sha256, coalesce(device_id, ''), created_at, updated_at, deleted_at, tokens_revoked_before`

func scanDeviceAccount(row pgx.Row) (DeviceAccount, error) {
	var da DeviceAccount
	err := row.Scan(&da.ID, &da.UserID, &da.PasswordSHA256, &da.DeviceID,
		&da.CreatedAt, &da.UpdatedAt, &da.DeletedAt, &da.TokensRevokedBefore)
	if err != nil {
		return DeviceAccount{}, mapErr(err)
	}
	return da, nil
}

// GetDeviceAccount returns a device account by id.
func (p *Postgres) GetDeviceAccount(ctx context.Context, id string) (DeviceAccount, error) {
	return scanDeviceAccount(p.pool.QueryRow(ctx,
		`SELECT `+deviceAccountColumns+` FROM device_accounts WHERE id = $1`, id))
}

// ListDeviceAccounts returns a user's live device accounts.
func (p *Postgres) ListDeviceAccounts(ctx context.Context, userID string) ([]DeviceAccount, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+deviceAccountColumns+` FROM device_accounts
		 WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []DeviceAccount
	for rows.Next() {
		da, err := scanDeviceAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, da)
	}
	return out, mapErr(rows.Err())
}

// ListAllDeviceAccounts returns a user's device accounts, deleted ones
// included -- see the interface doc for why ListDeviceAccounts's own
// exclusion does not serve the Debug tab.
func (p *Postgres) ListAllDeviceAccounts(ctx context.Context, userID string) ([]DeviceAccount, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+deviceAccountColumns+` FROM device_accounts
		 WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []DeviceAccount
	for rows.Next() {
		da, err := scanDeviceAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, da)
	}
	return out, mapErr(rows.Err())
}

// DeleteDeviceAccount marks a device account deleted.
func (p *Postgres) DeleteDeviceAccount(ctx context.Context, userID, id string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE device_accounts SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReassignDeviceAccount moves a device account to a different user. The
// device_accounts.user_id foreign key does the "does the target user exist"
// check for free: a violation maps to ErrNotFound (see mapErr).
func (p *Postgres) ReassignDeviceAccount(ctx context.Context, deviceAccountID, newUserID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var oldUserID string
	err = tx.QueryRow(ctx, `
		UPDATE device_accounts d SET user_id = $2, updated_at = now()
		FROM device_accounts old
		WHERE d.id = $1 AND old.id = d.id
		RETURNING old.user_id`, deviceAccountID, newUserID).Scan(&oldUserID)
	if err != nil {
		return mapErr(err)
	}
	if oldUserID != newUserID {
		if _, err := tx.Exec(ctx, `
			DELETE FROM users u
			WHERE u.id = $1 AND u.provisional
			  AND NOT EXISTS (SELECT 1 FROM device_accounts d WHERE d.user_id = u.id)`,
			oldUserID); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit(ctx)
}

// UndeleteDeviceAccount clears deleted_at.
func (p *Postgres) UndeleteDeviceAccount(ctx context.Context, deviceAccountID string) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE device_accounts SET deleted_at = NULL, updated_at = now()
		WHERE id = $1 AND deleted_at IS NOT NULL`, deviceAccountID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeDeviceAccountTokens invalidates every token minted before `before`.
func (p *Postgres) RevokeDeviceAccountTokens(ctx context.Context, deviceAccountID string, before time.Time) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE device_accounts SET tokens_revoked_before = $2, updated_at = now()
		WHERE id = $1`, deviceAccountID, before)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- friend codes --------------------------------------------------------

// GetFriendCode returns a user's friend code.
func (p *Postgres) GetFriendCode(ctx context.Context, userID string) (FriendCode, error) {
	var fc FriendCode
	err := p.pool.QueryRow(ctx, `
		SELECT user_id, code, created_at, updated_at, regenerable_at
		FROM friend_codes WHERE user_id = $1`, userID).
		Scan(&fc.UserID, &fc.Code, &fc.CreatedAt, &fc.UpdatedAt, &fc.RegenerableAt)
	return fc, mapErr(err)
}

// SetFriendCode writes a user's friend code.
func (p *Postgres) SetFriendCode(ctx context.Context, fc FriendCode) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO friend_codes (user_id, code, created_at, updated_at, regenerable_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (user_id) DO UPDATE SET
			code = EXCLUDED.code,
			updated_at = EXCLUDED.updated_at,
			regenerable_at = EXCLUDED.regenerable_at`,
		fc.UserID, fc.Code, fc.CreatedAt, fc.UpdatedAt, fc.RegenerableAt)
	return mapErr(err)
}

// ---- friendships ---------------------------------------------------------

// AddFriendship writes both directions and bumps both users' friends_updated_at.
func (p *Postgres) AddFriendship(ctx context.Context, a, b string, at time.Time) error {
	if a == b {
		return fmt.Errorf("%w: a user cannot befriend themselves", ErrConflict)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO friendships (user_id, friend_id, became_friend_at)
		VALUES ($1,$2,$3), ($2,$1,$3)
		ON CONFLICT (user_id, friend_id) DO NOTHING`, a, b, at)
	if err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET friends_updated_at = $2, updated_at = $2 WHERE id = ANY($1)`,
		[]string{a, b}, at); err != nil {
		return mapErr(err)
	}
	return tx.Commit(ctx)
}

// RemoveFriendship removes both directions.
func (p *Postgres) RemoveFriendship(ctx context.Context, a, b string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		DELETE FROM friendships
		WHERE (user_id = $1 AND friend_id = $2) OR (user_id = $2 AND friend_id = $1)`, a, b)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET friends_updated_at = now(), updated_at = now() WHERE id = ANY($1)`,
		[]string{a, b}); err != nil {
		return mapErr(err)
	}
	return tx.Commit(ctx)
}

// ListFriends returns one user's friendships.
func (p *Postgres) ListFriends(ctx context.Context, userID string, limit int) ([]Friendship, error) {
	sql := `SELECT user_id, friend_id, favorite, became_friend_at, extras
	        FROM friendships WHERE user_id = $1
	        ORDER BY favorite DESC, became_friend_at`
	args := []any{userID}
	if limit > 0 {
		args = append(args, limit)
		sql += " LIMIT $2"
	}
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Friendship
	for rows.Next() {
		f, err := scanFriendship(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, mapErr(rows.Err())
}

// GetFriendship returns one direction of a friendship.
func (p *Postgres) GetFriendship(ctx context.Context, userID, friendID string) (Friendship, error) {
	return scanFriendship(p.pool.QueryRow(ctx, `
		SELECT user_id, friend_id, favorite, became_friend_at, extras
		FROM friendships WHERE user_id = $1 AND friend_id = $2`, userID, friendID))
}

func scanFriendship(row pgx.Row) (Friendship, error) {
	var f Friendship
	var extras []byte
	if err := row.Scan(&f.UserID, &f.FriendID, &f.Favorite, &f.BecameFriendAt, &extras); err != nil {
		return Friendship{}, mapErr(err)
	}
	if err := json.Unmarshal(extras, &f.Extras); err != nil {
		return Friendship{}, fmt.Errorf("decode friendship extras %s -> %s: %w", f.UserID, f.FriendID, err)
	}
	return f, nil
}

// SetFriendshipExtras replaces the extras on one direction only.
func (p *Postgres) SetFriendshipExtras(ctx context.Context, userID, friendID string, extras ExtrasByScope) error {
	if extras == nil {
		extras = ExtrasByScope{}
	}
	buf, err := json.Marshal(extras)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE friendships SET extras = $3
		WHERE user_id = $1 AND friend_id = $2`, userID, friendID, buf)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetFavorite sets the favourite flag on one direction only.
func (p *Postgres) SetFavorite(ctx context.Context, userID, friendID string, favorite bool) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE friendships SET favorite = $3
		WHERE user_id = $1 AND friend_id = $2`, userID, friendID, favorite)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- NPLN Ugcstore documents ----------------------------------------------

// GetUGCDocument returns a Ugcstore document by name, or ErrNotFound.
func (p *Postgres) GetUGCDocument(ctx context.Context, name string) (UGCDocument, error) {
	var d UGCDocument
	err := p.pool.QueryRow(ctx, `
		SELECT name, owner, fields, create_time, update_time
		FROM ugc_documents WHERE name = $1`, name).
		Scan(&d.Name, &d.Owner, &d.Fields, &d.CreateTime, &d.UpdateTime)
	return d, mapErr(err)
}

// ListUGCDocuments returns every document whose name starts with prefix,
// newest name first. The prefix is matched with starts_with rather than LIKE so
// an underscore or percent sign in a name is never a wildcard.
func (p *Postgres) ListUGCDocuments(ctx context.Context, prefix string) ([]UGCDocument, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT name, owner, fields, create_time, update_time
		FROM ugc_documents WHERE starts_with(name, $1) ORDER BY name DESC`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UGCDocument
	for rows.Next() {
		var d UGCDocument
		if err := rows.Scan(&d.Name, &d.Owner, &d.Fields, &d.CreateTime, &d.UpdateTime); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PutUGCDocument creates or replaces a Ugcstore document.
func (p *Postgres) PutUGCDocument(ctx context.Context, d UGCDocument) error {
	if d.Fields == nil {
		d.Fields = []byte{}
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO ugc_documents (name, owner, fields, create_time, update_time)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE
		SET owner = EXCLUDED.owner, fields = EXCLUDED.fields,
		    create_time = EXCLUDED.create_time, update_time = EXCLUDED.update_time`,
		d.Name, d.Owner, d.Fields, d.CreateTime, d.UpdateTime)
	return mapErr(err)
}

// CompareAndPutUGCDocuments commits the entire batch only if every revision matches.
func (p *Postgres) CompareAndPutUGCDocuments(ctx context.Context, changes []UGCDocumentCAS) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	seen := map[string]bool{}
	for _, change := range changes {
		d := change.Document
		if seen[d.Name] {
			return ErrConflict
		}
		seen[d.Name] = true
		if d.Fields == nil {
			d.Fields = []byte{}
		}
		var tag pgconn.CommandTag
		if change.PreviousUpdateTime == nil {
			tag, err = tx.Exec(ctx, `INSERT INTO ugc_documents (name, owner, fields, create_time, update_time)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (name) DO NOTHING`, d.Name, d.Owner, d.Fields, d.CreateTime, d.UpdateTime)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE ugc_documents SET owner=$2, fields=$3, create_time=$4, update_time=$5
			WHERE name=$1 AND update_time=$6`, d.Name, d.Owner, d.Fields, d.CreateTime, d.UpdateTime, *change.PreviousUpdateTime)
		}
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	return mapErr(tx.Commit(ctx))
}

// DeleteUGCDocument removes a Ugcstore document, or returns ErrNotFound.
func (p *Postgres) DeleteUGCDocument(ctx context.Context, name string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM ugc_documents WHERE name = $1`, name)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- NEX game-server records ----------------------------------------------

// PutNEXRecord creates or replaces a record.
func (p *Postgres) PutNEXRecord(ctx context.Context, r NEXRecord) error {
	if r.Value == nil {
		r.Value = []byte{}
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO nex_records (title, kind, key, owner, value, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (title, kind, key) DO UPDATE
		SET owner = EXCLUDED.owner, value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		r.Title, r.Kind, r.Key, r.Owner, r.Value, r.UpdatedAt)
	return mapErr(err)
}

// GetNEXRecord returns a record, or ErrNotFound.
func (p *Postgres) GetNEXRecord(ctx context.Context, title, kind, key string) (NEXRecord, error) {
	var r NEXRecord
	err := p.pool.QueryRow(ctx, `
		SELECT title, kind, key, owner, value, updated_at
		FROM nex_records WHERE title = $1 AND kind = $2 AND key = $3`, title, kind, key).
		Scan(&r.Title, &r.Kind, &r.Key, &r.Owner, &r.Value, &r.UpdatedAt)
	return r, mapErr(err)
}

// ListNEXRecords returns a kind's records, most recently updated first.
func (p *Postgres) ListNEXRecords(ctx context.Context, q NEXRecordQuery) ([]NEXRecord, error) {
	limit := q.Limit
	if limit <= 0 || limit > DefaultNEXRecordLimit {
		limit = DefaultNEXRecordLimit
	}
	rows, err := p.pool.Query(ctx, `
		SELECT title, kind, key, owner, value, updated_at
		FROM nex_records
		WHERE title = $1 AND kind = $2 AND ($3 = '' OR owner = $3)
		ORDER BY updated_at DESC, key
		LIMIT $4`, q.Title, q.Kind, q.Owner, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []NEXRecord
	for rows.Next() {
		var r NEXRecord
		if err := rows.Scan(&r.Title, &r.Kind, &r.Key, &r.Owner, &r.Value, &r.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// DeleteNEXRecord removes a record, or returns ErrNotFound.
func (p *Postgres) DeleteNEXRecord(ctx context.Context, title, kind, key string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM nex_records WHERE title = $1 AND kind = $2 AND key = $3`,
		title, kind, key)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Nintendo Account refresh tokens -------------------------------------

// CreateNARefreshToken stores a refresh token, and drops any that expired
// before t.CreatedAt.
func (p *Postgres) CreateNARefreshToken(ctx context.Context, t NARefreshToken) error {
	scope := t.Scope
	if scope == nil {
		scope = []string{}
	}
	buf, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	if _, err := p.pool.Exec(ctx,
		`DELETE FROM na_refresh_tokens WHERE expires_at <= $1`, t.CreatedAt); err != nil {
		return mapErr(err)
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO na_refresh_tokens (token_sha256, user_id, scope, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		t.TokenSHA256, t.UserID, buf, t.ExpiresAt, t.CreatedAt)
	return mapErr(err)
}

// GetNARefreshToken returns the token with this hash, or ErrNotFound if there
// is none or it expired at or before now.
func (p *Postgres) GetNARefreshToken(ctx context.Context, tokenSHA256 string, now time.Time) (NARefreshToken, error) {
	var t NARefreshToken
	var scope []byte
	err := p.pool.QueryRow(ctx, `
		SELECT token_sha256, user_id, scope, expires_at, created_at
		FROM na_refresh_tokens WHERE token_sha256 = $1 AND expires_at > $2`,
		tokenSHA256, now).Scan(&t.TokenSHA256, &t.UserID, &scope, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return NARefreshToken{}, mapErr(err)
	}
	if err := json.Unmarshal(scope, &t.Scope); err != nil {
		return NARefreshToken{}, fmt.Errorf("decode refresh token scope: %w", err)
	}
	return t, nil
}

// ---- friend requests -----------------------------------------------------

const friendRequestColumns = `id, from_user_id, to_user_id, state, created_at, updated_at`

func scanFriendRequest(row pgx.Row) (FriendRequest, error) {
	var r FriendRequest
	err := row.Scan(&r.ID, &r.FromUserID, &r.ToUserID, &r.State, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return FriendRequest{}, mapErr(err)
	}
	return r, nil
}

// CreateFriendRequest inserts a request, or returns the existing pending one.
func (p *Postgres) CreateFriendRequest(ctx context.Context, r FriendRequest) (FriendRequest, error) {
	existing, err := p.FindPendingRequest(ctx, r.FromUserID, r.ToUserID)
	switch {
	case err == nil:
		return existing, nil
	case errors.Is(err, ErrNotFound):
	default:
		return FriendRequest{}, err
	}

	return scanFriendRequest(p.pool.QueryRow(ctx, `
		INSERT INTO friend_requests (id, from_user_id, to_user_id, state, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5)
		RETURNING `+friendRequestColumns,
		r.ID, r.FromUserID, r.ToUserID, r.State, r.CreatedAt))
}

// GetFriendRequest returns a request by id.
func (p *Postgres) GetFriendRequest(ctx context.Context, id string) (FriendRequest, error) {
	return scanFriendRequest(p.pool.QueryRow(ctx,
		`SELECT `+friendRequestColumns+` FROM friend_requests WHERE id = $1`, id))
}

// SetFriendRequestState resolves a request.
func (p *Postgres) SetFriendRequestState(ctx context.Context, id, state string, at time.Time) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE friend_requests SET state = $2, updated_at = $3
		WHERE id = $1 AND state = 'PENDING'`, id, state, at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFriendRequests returns a user's inbox or outbox.
func (p *Postgres) ListFriendRequests(ctx context.Context, userID string, incoming, pendingOnly bool) ([]FriendRequest, error) {
	col := "from_user_id"
	if incoming {
		col = "to_user_id"
	}
	sql := `SELECT ` + friendRequestColumns + ` FROM friend_requests WHERE ` + col + ` = $1`
	if pendingOnly {
		sql += ` AND state = 'PENDING'`
	}
	sql += ` ORDER BY created_at DESC`

	rows, err := p.pool.Query(ctx, sql, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []FriendRequest
	for rows.Next() {
		r, err := scanFriendRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// FindPendingRequest returns a pending request for an ordered pair.
func (p *Postgres) FindPendingRequest(ctx context.Context, from, to string) (FriendRequest, error) {
	return scanFriendRequest(p.pool.QueryRow(ctx,
		`SELECT `+friendRequestColumns+` FROM friend_requests
		 WHERE from_user_id = $1 AND to_user_id = $2 AND state = 'PENDING'`, from, to))
}

// ---- blocks --------------------------------------------------------------

// AddBlock blocks a user.
func (p *Postgres) AddBlock(ctx context.Context, userID, blockedID string, at time.Time) error {
	if userID == blockedID {
		return fmt.Errorf("%w: a user cannot block themselves", ErrConflict)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO blocks (user_id, blocked_user_id, created_at) VALUES ($1,$2,$3)
		ON CONFLICT (user_id, blocked_user_id) DO NOTHING`, userID, blockedID, at); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET blocks_updated_at = $2, updated_at = $2 WHERE id = $1`,
		userID, at); err != nil {
		return mapErr(err)
	}
	return tx.Commit(ctx)
}

// RemoveBlock unblocks a user.
func (p *Postgres) RemoveBlock(ctx context.Context, userID, blockedID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`DELETE FROM blocks WHERE user_id = $1 AND blocked_user_id = $2`, userID, blockedID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET blocks_updated_at = now(), updated_at = now() WHERE id = $1`,
		userID); err != nil {
		return mapErr(err)
	}
	return tx.Commit(ctx)
}

// ListBlocks returns who a user has blocked.
func (p *Postgres) ListBlocks(ctx context.Context, userID string) ([]Block, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT user_id, blocked_user_id, created_at FROM blocks
		WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.UserID, &b.BlockedUserID, &b.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, b)
	}
	return out, mapErr(rows.Err())
}

// IsBlockedEitherWay reports whether either user has blocked the other.
func (p *Postgres) IsBlockedEitherWay(ctx context.Context, a, b string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM blocks
			WHERE (user_id = $1 AND blocked_user_id = $2)
			   OR (user_id = $2 AND blocked_user_id = $1))`, a, b).Scan(&exists)
	return exists, mapErr(err)
}

// IsBlocking reports whether blocker has blocked blocked, in that direction
// only.
func (p *Postgres) IsBlocking(ctx context.Context, blocker, blocked string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM blocks
			WHERE user_id = $1 AND blocked_user_id = $2)`, blocker, blocked).Scan(&exists)
	return exists, mapErr(err)
}

// ---- presence ------------------------------------------------------------

const presenceColumns = `
	user_id, state, coalesce(app_id, ''), coalesce(presence_group_id, ''),
	app_field, acd_index, extras, reported_at, last_seen_at, logout_at`

func scanPresence(row pgx.Row) (Presence, error) {
	var pr Presence
	var extras []byte
	// reported_at is NULL until the console reports something, so it scans into
	// a pointer. A zero ReportedAt is how the read path recognises "never
	// reported" as distinct from "reported OFFLINE".
	var reportedAt *time.Time
	err := row.Scan(&pr.UserID, &pr.State, &pr.AppID, &pr.PresenceGroupID,
		&pr.AppField, &pr.ACDIndex, &extras, &reportedAt, &pr.LastSeenAt, &pr.LogoutAt)
	if err != nil {
		return Presence{}, mapErr(err)
	}
	if reportedAt != nil {
		pr.ReportedAt = *reportedAt
	}
	if err := json.Unmarshal(extras, &pr.Extras); err != nil {
		return Presence{}, fmt.Errorf("decode presence extras for %s: %w", pr.UserID, err)
	}
	if pr.Extras == nil {
		pr.Extras = EmptyExtras()
	}
	return pr, nil
}

// GetPresence returns a user's presence, or an OFFLINE zero value if they have
// never reported one.
func (p *Postgres) GetPresence(ctx context.Context, userID string) (Presence, error) {
	pr, err := scanPresence(p.pool.QueryRow(ctx,
		`SELECT `+presenceColumns+` FROM presence WHERE user_id = $1`, userID))
	if errors.Is(err, ErrNotFound) {
		return offlinePresence(userID), nil
	}
	return pr, err
}

// GetPresences returns presence for several users at once.
func (p *Postgres) GetPresences(ctx context.Context, userIDs []string) (map[string]Presence, error) {
	out := make(map[string]Presence, len(userIDs))
	for _, id := range userIDs {
		out[id] = offlinePresence(id)
	}
	if len(userIDs) == 0 {
		return out, nil
	}

	rows, err := p.pool.Query(ctx,
		`SELECT `+presenceColumns+` FROM presence WHERE user_id = ANY($1)`, userIDs)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		pr, err := scanPresence(rows)
		if err != nil {
			return nil, err
		}
		out[pr.UserID] = pr
	}
	return out, mapErr(rows.Err())
}

func offlinePresence(userID string) Presence {
	return Presence{UserID: userID, State: StateOffline, Extras: EmptyExtras()}
}

// UpdatePresence applies a patch, creating the row if needed.
func (p *Postgres) UpdatePresence(ctx context.Context, userID string, patch PresencePatch) (Presence, error) {
	// Insert a bare row first so the UPDATE below always has something to
	// change. ON CONFLICT DO NOTHING makes this a no-op for an existing user.
	if _, err := p.pool.Exec(ctx, `
		INSERT INTO presence (user_id, state, extras) VALUES ($1, $2, '{}'::jsonb)
		ON CONFLICT (user_id) DO NOTHING`, userID, StateOffline); err != nil {
		return Presence{}, mapErr(err)
	}

	var sets []string
	args := []any{userID}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	// LastSeenAt tracks the reported state's own timestamp when there is one --
	// consistent with Memory.UpdatePresence, and with what "last seen" ought to
	// mean: when the console said this happened, not when this write landed.
	// Falling back to the database's own clock only when nothing was reported
	// matches TouchPresence, which never sets ReportedAt at all.
	if patch.ReportedAt != nil {
		add("last_seen_at", *patch.ReportedAt)
	} else {
		sets = append(sets, "last_seen_at = now()")
	}

	if patch.State != nil {
		add("state", *patch.State)
	}
	if patch.AppID != nil {
		add("app_id", nullIfEmpty(*patch.AppID))
	}
	if patch.PresenceGroupID != nil {
		add("presence_group_id", nullIfEmpty(*patch.PresenceGroupID))
	}
	if patch.AppField != nil {
		add("app_field", *patch.AppField)
	}
	if patch.ACDIndex != nil {
		add("acd_index", *patch.ACDIndex)
	}
	if patch.Extras != nil {
		buf, err := json.Marshal(*patch.Extras)
		if err != nil {
			return Presence{}, err
		}
		add("extras", buf)
	}
	if patch.ReportedAt != nil {
		add("reported_at", *patch.ReportedAt)
	}
	if patch.LogoutAt != nil {
		add("logout_at", *patch.LogoutAt)
	}

	q := `UPDATE presence SET ` + strings.Join(sets, ", ") +
		` WHERE user_id = $1 RETURNING ` + presenceColumns
	return scanPresence(p.pool.QueryRow(ctx, q, args...))
}

// TouchPresence records that a user made an authenticated request.
func (p *Postgres) TouchPresence(ctx context.Context, userID string, at time.Time) error {
	// Touch never writes reported_at: it records that the console is alive, not
	// that it said anything about its state.
	_, err := p.pool.Exec(ctx, `
		INSERT INTO presence (user_id, state, extras, last_seen_at)
		VALUES ($1, $2, '{}'::jsonb, $3)
		ON CONFLICT (user_id) DO UPDATE SET last_seen_at = GREATEST(presence.last_seen_at, $3)`,
		userID, StateOffline, at)
	return mapErr(err)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- images --------------------------------------------------------------

// CreateImage records an uploaded image.
func (p *Postgres) CreateImage(ctx context.Context, img Image) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO images (id, owner_id, format, width, height, size_bytes, rel_path,
		                    created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		img.ID, img.OwnerID, img.Format, img.Width, img.Height, img.SizeBytes,
		img.RelPath, img.CreatedAt)
	return mapErr(err)
}

// GetImage returns an image by id.
func (p *Postgres) GetImage(ctx context.Context, id string) (Image, error) {
	var img Image
	err := p.pool.QueryRow(ctx, `
		SELECT id, owner_id, format, width, height, size_bytes, rel_path, created_at, updated_at
		FROM images WHERE id = $1`, id).
		Scan(&img.ID, &img.OwnerID, &img.Format, &img.Width, &img.Height,
			&img.SizeBytes, &img.RelPath, &img.CreatedAt, &img.UpdatedAt)
	return img, mapErr(err)
}

// Compile-time check that Postgres satisfies the interface.
var _ Store = (*Postgres)(nil)

// ---- splatfests ----------------------------------------------------------

const festColumns = `id, resource, revision, start_at, regions, pack_keys, enabled, created_at, updated_at`

func (p *Postgres) UpsertFest(ctx context.Context, f Fest) error {
	// A nil slice becomes SQL NULL, which the NOT NULL columns reject -- while
	// the in-memory store stores nil happily. That divergence is exactly what
	// the shared contract test exists to catch, and it caught this one: a fest
	// with no pack keys yet (every fest, before its pack is built) failed on
	// PostgreSQL and succeeded in memory. Normalised here so "no keys" means
	// the same thing in both.
	if f.Regions == nil {
		f.Regions = []string{}
	}
	if f.PackKeys == nil {
		f.PackKeys = []string{}
	}

	_, err := p.pool.Exec(ctx,
		`INSERT INTO npln_fests (id, resource, revision, start_at, regions, pack_keys, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (id) DO UPDATE SET
		     resource   = EXCLUDED.resource,
		     revision   = EXCLUDED.revision,
		     start_at   = EXCLUDED.start_at,
		     regions    = EXCLUDED.regions,
		     pack_keys  = EXCLUDED.pack_keys,
		     enabled    = EXCLUDED.enabled,
		     updated_at = now()`,
		f.ID, f.Resource, f.Revision, f.StartAt, f.Regions, f.PackKeys, f.Enabled)
	return mapErr(err)
}

func (p *Postgres) GetFest(ctx context.Context, id string) (Fest, error) {
	return scanFest(p.pool.QueryRow(ctx,
		`SELECT `+festColumns+` FROM npln_fests WHERE id = $1`, id))
}

func (p *Postgres) ListFests(ctx context.Context) ([]Fest, error) {
	// Soonest first, id as the tiebreak: the in-memory store orders the same
	// way, and the contract test compares them.
	rows, err := p.pool.Query(ctx,
		`SELECT `+festColumns+` FROM npln_fests ORDER BY start_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Fest
	for rows.Next() {
		f, err := scanFest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, mapErr(rows.Err())
}

func (p *Postgres) DeleteFest(ctx context.Context, id string) error {
	// No rows affected is not an error: the dashboard's delete button should be
	// idempotent, and the in-memory store's delete is too.
	_, err := p.pool.Exec(ctx, `DELETE FROM npln_fests WHERE id = $1`, id)
	return mapErr(err)
}

// ---- splatfest entries ---------------------------------------------------

const festEntryColumns = `fest_id, user_id, team, region, created_at, updated_at`

func (p *Postgres) UpsertFestEntry(ctx context.Context, e FestEntry) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO npln_fest_entries (fest_id, user_id, team, region)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (fest_id, user_id) DO UPDATE SET
		     team       = EXCLUDED.team,
		     region     = EXCLUDED.region,
		     updated_at = now()`,
		e.FestID, e.UserID, e.Team, e.Region)
	return mapErr(err)
}

func (p *Postgres) GetFestEntry(ctx context.Context, festID, userID string) (FestEntry, error) {
	return scanFestEntry(p.pool.QueryRow(ctx,
		`SELECT `+festEntryColumns+` FROM npln_fest_entries
		 WHERE fest_id = $1 AND user_id = $2`, festID, userID))
}

func (p *Postgres) ListFestEntries(ctx context.Context, festID string) ([]FestEntry, error) {
	// Oldest first, user id as the tiebreak: the in-memory store orders the same
	// way and the contract test compares them.
	rows, err := p.pool.Query(ctx,
		`SELECT `+festEntryColumns+` FROM npln_fest_entries
		 WHERE fest_id = $1 ORDER BY created_at, user_id`, festID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []FestEntry
	for rows.Next() {
		e, err := scanFestEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

func (p *Postgres) RecordFestBattle(ctx context.Context, b FestBattle) error {
	ink, err := json.Marshal(b.Ink)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO npln_fest_battles
		(fest_id, battle_id, mode, winning_team, ink, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (fest_id, battle_id) DO NOTHING`,
		b.FestID, b.BattleID, b.Mode, b.WinningTeam, ink, b.FinishedAt)
	return mapErr(err)
}

func (p *Postgres) ListFestBattles(ctx context.Context, festID string) ([]FestBattle, error) {
	rows, err := p.pool.Query(ctx, `SELECT fest_id, battle_id, mode, winning_team, ink, finished_at
		FROM npln_fest_battles WHERE fest_id = $1 ORDER BY finished_at, battle_id`, festID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []FestBattle
	for rows.Next() {
		var b FestBattle
		var ink []byte
		if err := rows.Scan(&b.FestID, &b.BattleID, &b.Mode, &b.WinningTeam, &ink, &b.FinishedAt); err != nil {
			return nil, mapErr(err)
		}
		if err := json.Unmarshal(ink, &b.Ink); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, mapErr(rows.Err())
}

func scanFestEntry(row interface{ Scan(...any) error }) (FestEntry, error) {
	var e FestEntry
	err := row.Scan(&e.FestID, &e.UserID, &e.Team, &e.Region, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return FestEntry{}, mapErr(err)
	}
	return e, nil
}

func scanFest(row interface{ Scan(...any) error }) (Fest, error) {
	var f Fest
	err := row.Scan(&f.ID, &f.Resource, &f.Revision, &f.StartAt,
		&f.Regions, &f.PackKeys, &f.Enabled, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return Fest{}, mapErr(err)
	}
	return f, nil
}

// ---- Salmon Run / Big Run schedule ---------------------------------------

const coopShiftColumns = `id, kind, stage_id, boss, weapons, kuma_weapon_id, reward_gear_id, rewards, reward_borders, is_special, start_at, end_at, created_at, updated_at`

func (p *Postgres) UpsertCoopShift(ctx context.Context, c CoopShift) error {
	// Same normalisation UpsertFest applies to Regions/PackKeys: a nil slice
	// becomes SQL NULL, which the NOT NULL columns reject while the in-memory
	// store accepts it happily. Normalised here so both implementations agree.
	if c.Weapons == nil {
		c.Weapons = []int64{}
	}
	if c.Rewards == nil {
		c.Rewards = []int64{}
	}
	if c.RewardBorders == nil {
		c.RewardBorders = []int64{}
	}

	_, err := p.pool.Exec(ctx,
		`INSERT INTO npln_coop_shifts (id, kind, stage_id, boss, weapons, kuma_weapon_id, reward_gear_id, rewards, reward_borders, is_special, start_at, end_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (id) DO UPDATE SET
		     kind           = EXCLUDED.kind,
		     stage_id       = EXCLUDED.stage_id,
		     boss           = EXCLUDED.boss,
		     weapons        = EXCLUDED.weapons,
		     kuma_weapon_id = EXCLUDED.kuma_weapon_id,
		     reward_gear_id = EXCLUDED.reward_gear_id,
		     rewards        = EXCLUDED.rewards,
		     reward_borders = EXCLUDED.reward_borders,
		     is_special     = EXCLUDED.is_special,
		     start_at       = EXCLUDED.start_at,
		     end_at         = EXCLUDED.end_at,
		     updated_at     = now()`,
		c.ID, c.Kind, c.StageID, c.Boss, c.Weapons, c.KumaWeaponID, c.RewardGearID, c.Rewards, c.RewardBorders, c.IsSpecial, c.StartAt, c.EndAt)
	return mapErr(err)
}

func (p *Postgres) GetCoopShift(ctx context.Context, id string) (CoopShift, error) {
	return scanCoopShift(p.pool.QueryRow(ctx,
		`SELECT `+coopShiftColumns+` FROM npln_coop_shifts WHERE id = $1`, id))
}

func (p *Postgres) ListCoopShifts(ctx context.Context) ([]CoopShift, error) {
	// Soonest first, id as the tiebreak: the in-memory store orders the same
	// way, and the contract test compares them.
	rows, err := p.pool.Query(ctx,
		`SELECT `+coopShiftColumns+` FROM npln_coop_shifts ORDER BY start_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []CoopShift
	for rows.Next() {
		c, err := scanCoopShift(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

func (p *Postgres) DeleteCoopShift(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM npln_coop_shifts WHERE id = $1`, id)
	return mapErr(err)
}

func scanCoopShift(row interface{ Scan(...any) error }) (CoopShift, error) {
	var c CoopShift
	err := row.Scan(&c.ID, &c.Kind, &c.StageID, &c.Boss, &c.Weapons, &c.KumaWeaponID,
		&c.RewardGearID, &c.Rewards, &c.RewardBorders, &c.IsSpecial, &c.StartAt, &c.EndAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return CoopShift{}, mapErr(err)
	}
	return c, nil
}

// ---- match history ---------------------------------------------------------

const matchColumns = `id, tenant, mode, host_user_id, player_ids, forced, created_at`

func (p *Postgres) CreateMatch(ctx context.Context, m Match) error {
	if m.PlayerIDs == nil {
		m.PlayerIDs = []string{}
	}
	// ON CONFLICT DO NOTHING: a match is recorded exactly once, at formation,
	// and session ids do not repeat within the queue's own lifetime -- but a
	// retried write after a network blip must not fail the caller (the match
	// already formed either way) or duplicate the row.
	_, err := p.pool.Exec(ctx,
		`INSERT INTO npln_matches (`+matchColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (id) DO NOTHING`,
		m.ID, m.Tenant, m.Mode, m.HostUserID, m.PlayerIDs, m.Forced, m.CreatedAt)
	return mapErr(err)
}

func (p *Postgres) ListMatches(ctx context.Context, limit int) ([]Match, error) {
	if limit <= 0 || limit > DefaultMatchListLimit {
		limit = DefaultMatchListLimit
	}
	return p.listMatches(ctx,
		`SELECT `+matchColumns+` FROM npln_matches ORDER BY created_at DESC LIMIT $1`, limit)
}

func (p *Postgres) ListMatchesForTenant(ctx context.Context, tenant string, limit int) ([]Match, error) {
	if limit <= 0 || limit > DefaultMatchListLimit {
		limit = DefaultMatchListLimit
	}
	return p.listMatches(ctx,
		`SELECT `+matchColumns+` FROM npln_matches WHERE tenant = $2 ORDER BY created_at DESC LIMIT $1`,
		limit, tenant)
}

func (p *Postgres) listMatches(ctx context.Context, query string, args ...any) ([]Match, error) {
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.Tenant, &m.Mode, &m.HostUserID,
			&m.PlayerIDs, &m.Forced, &m.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, m)
	}
	return out, mapErr(rows.Err())
}

// ---- OLSC save data archives (phase 4) -------------------------------------

const saveDataArchiveColumns = `id, nsa_id, application_id, device_id, device_serial,
	data_size, description, series_id, datatype, status, auto_backup, num_of_partitions,
	launch_required_version, previous_save_data_archive_id, encoded_digest,
	saved_at, finished_at, created_at, updated_at`

func (p *Postgres) CreateSaveDataArchive(ctx context.Context, a SaveDataArchive) (SaveDataArchive, error) {
	if a.Status == "" {
		a.Status = "uploading"
	}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO scsi_save_data_archives
		     (id, nsa_id, application_id, device_id, device_serial, data_size, description,
		      series_id, datatype, status, auto_backup, num_of_partitions,
		      launch_required_version, previous_save_data_archive_id, encoded_digest, saved_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		 RETURNING created_at, updated_at`,
		a.ID, a.NSAID, a.ApplicationID, a.DeviceID, a.DeviceSerial, a.DataSize, a.Description,
		a.SeriesID, a.Datatype, a.Status, a.AutoBackup, a.NumOfPartitions,
		a.LaunchRequiredVersion, a.PreviousSaveDataArchiveID, a.EncodedDigest, a.SavedAt,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return SaveDataArchive{}, mapErr(err)
	}
	return a, nil
}

func (p *Postgres) GetSaveDataArchive(ctx context.Context, id string) (SaveDataArchive, error) {
	return scanSaveDataArchive(p.pool.QueryRow(ctx,
		`SELECT `+saveDataArchiveColumns+` FROM scsi_save_data_archives WHERE id = $1`, id))
}

func (p *Postgres) ListSaveDataArchives(ctx context.Context, q SaveDataArchiveQuery) ([]SaveDataArchive, error) {
	// Most recently saved first, id as the tiebreak: the in-memory store
	// orders the same way, and the contract test compares them.
	rows, err := p.pool.Query(ctx,
		`SELECT `+saveDataArchiveColumns+` FROM scsi_save_data_archives
		 WHERE nsa_id = $1
		   AND ($2 = '' OR application_id = $2)
		   AND ($3 = '' OR device_id = $3)
		 ORDER BY saved_at DESC, id`, q.NSAID, q.ApplicationID, q.DeviceID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []SaveDataArchive
	for rows.Next() {
		a, err := scanSaveDataArchive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

func (p *Postgres) UpdateSaveDataArchive(ctx context.Context, id string, patch SaveDataArchivePatch) (SaveDataArchive, error) {
	a, err := p.GetSaveDataArchive(ctx, id)
	if err != nil {
		return SaveDataArchive{}, err
	}
	if patch.Status != nil {
		a.Status = *patch.Status
	}
	if patch.EncodedDigest != nil {
		a.EncodedDigest = *patch.EncodedDigest
	}
	if patch.FinishedAt != nil {
		a.FinishedAt = patch.FinishedAt
	}
	if patch.DataSize != nil {
		a.DataSize = *patch.DataSize
	}
	if patch.NumOfPartitions != nil {
		a.NumOfPartitions = *patch.NumOfPartitions
	}
	_, err = p.pool.Exec(ctx,
		`UPDATE scsi_save_data_archives SET
		     status = $2, encoded_digest = $3, finished_at = $4,
		     data_size = $5, num_of_partitions = $6, updated_at = now()
		 WHERE id = $1`,
		id, a.Status, a.EncodedDigest, a.FinishedAt, a.DataSize, a.NumOfPartitions)
	if err != nil {
		return SaveDataArchive{}, mapErr(err)
	}
	return p.GetSaveDataArchive(ctx, id)
}

func (p *Postgres) DeleteSaveDataArchive(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM scsi_save_data_archives WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSaveDataArchive(row interface{ Scan(...any) error }) (SaveDataArchive, error) {
	var a SaveDataArchive
	err := row.Scan(&a.ID, &a.NSAID, &a.ApplicationID, &a.DeviceID, &a.DeviceSerial,
		&a.DataSize, &a.Description, &a.SeriesID, &a.Datatype, &a.Status, &a.AutoBackup,
		&a.NumOfPartitions, &a.LaunchRequiredVersion, &a.PreviousSaveDataArchiveID,
		&a.EncodedDigest, &a.SavedAt, &a.FinishedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return SaveDataArchive{}, mapErr(err)
	}
	return a, nil
}

const componentFileColumns = `id, archive_id, index, datatype, status, archive_size,
	encoded_archive_digest, blob_key, created_at, updated_at`

func (p *Postgres) CreateComponentFile(ctx context.Context, f ComponentFile) (ComponentFile, error) {
	if f.Status == "" {
		f.Status = "uploading"
	}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO scsi_component_files
		     (id, archive_id, index, datatype, status, archive_size, encoded_archive_digest, blob_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at, updated_at`,
		f.ID, f.ArchiveID, f.Index, f.Datatype, f.Status, f.ArchiveSize,
		f.EncodedArchiveDigest, f.BlobKey,
	).Scan(&f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return ComponentFile{}, mapErr(err)
	}
	return f, nil
}

func (p *Postgres) GetComponentFile(ctx context.Context, id string) (ComponentFile, error) {
	return scanComponentFile(p.pool.QueryRow(ctx,
		`SELECT `+componentFileColumns+` FROM scsi_component_files WHERE id = $1`, id))
}

func (p *Postgres) ListComponentFiles(ctx context.Context, archiveID string) ([]ComponentFile, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+componentFileColumns+` FROM scsi_component_files
		 WHERE archive_id = $1 ORDER BY index`, archiveID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []ComponentFile
	for rows.Next() {
		f, err := scanComponentFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, mapErr(rows.Err())
}

func (p *Postgres) UpdateComponentFile(ctx context.Context, id string, patch ComponentFilePatch) (ComponentFile, error) {
	f, err := p.GetComponentFile(ctx, id)
	if err != nil {
		return ComponentFile{}, err
	}
	if patch.Status != nil {
		f.Status = *patch.Status
	}
	if patch.ArchiveSize != nil {
		f.ArchiveSize = *patch.ArchiveSize
	}
	if patch.EncodedArchiveDigest != nil {
		f.EncodedArchiveDigest = *patch.EncodedArchiveDigest
	}
	_, err = p.pool.Exec(ctx,
		`UPDATE scsi_component_files SET
		     status = $2, archive_size = $3, encoded_archive_digest = $4, updated_at = now()
		 WHERE id = $1`,
		id, f.Status, f.ArchiveSize, f.EncodedArchiveDigest)
	if err != nil {
		return ComponentFile{}, mapErr(err)
	}
	return p.GetComponentFile(ctx, id)
}

func scanComponentFile(row interface{ Scan(...any) error }) (ComponentFile, error) {
	var f ComponentFile
	err := row.Scan(&f.ID, &f.ArchiveID, &f.Index, &f.Datatype, &f.Status, &f.ArchiveSize,
		&f.EncodedArchiveDigest, &f.BlobKey, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return ComponentFile{}, mapErr(err)
	}
	return f, nil
}

// ---- operator debugging ----------------------------------------------------

// Stats counts rows across the main tables in one query, so the numbers are
// a consistent snapshot rather than drifting between separate round trips
// under concurrent writes.
func (p *Postgres) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := p.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM users WHERE NOT provisional),
		(SELECT count(*) FROM users WHERE provisional),
		(SELECT count(*) FROM users WHERE deleted),
		(SELECT count(*) FROM device_accounts),
		(SELECT count(*) FROM device_accounts WHERE deleted_at IS NOT NULL),
		(SELECT count(*) FROM devices),
		(SELECT count(*) FROM friendships),
		(SELECT count(*) FROM npln_fests),
		(SELECT count(*) FROM npln_coop_shifts),
		(SELECT count(*) FROM npln_matches),
		(SELECT count(*) FROM images),
		(SELECT count(*) FROM scsi_save_data_archives)
	`).Scan(&s.Users, &s.ProvisionalUsers, &s.DeletedUsers, &s.DeviceAccounts, &s.DeletedDeviceAccounts,
		&s.Devices, &s.Friendships, &s.Fests, &s.CoopShifts, &s.Matches, &s.Images,
		&s.SaveDataArchives)
	return s, mapErr(err)
}
