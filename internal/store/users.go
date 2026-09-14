package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"
)

type User struct {
	ID               int64
	Username         string
	FirstName        string
	Balance          int64
	IsAgent          bool
	IsBlocked        bool
	ReferralCode     string
	ReferredBy       int64 // 0 = nobody
	ReferralRewarded bool
	TrialUsed        bool
	CreatedAt        int64
}

const userColumns = `id, username, first_name, balance, is_agent, is_blocked, referral_code,
	COALESCE(referred_by, 0), referral_rewarded, trial_used, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (User, error) {
	var u User
	var agent, blocked, rewarded, trial int
	err := r.Scan(&u.ID, &u.Username, &u.FirstName, &u.Balance, &agent, &blocked, &u.ReferralCode,
		&u.ReferredBy, &rewarded, &trial, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	u.IsAgent, u.IsBlocked, u.ReferralRewarded, u.TrialUsed = agent == 1, blocked == 1, rewarded == 1, trial == 1
	return u, err
}

// UpsertUser records a Telegram user on every interaction, refreshing the
// display name. The referrer is only ever set on the very first insert: a
// referral link opened by someone who already uses the bot must not
// re-attribute them, or anyone could farm rewards by re-sharing links.
func (s *Store) UpsertUser(ctx context.Context, id int64, username, firstName string, referrerCode string) (User, bool, error) {
	var created bool
	var out User
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		existing, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET username = ?, first_name = ? WHERE id = ?`, username, firstName, id); err != nil {
				return err
			}
			existing.Username, existing.FirstName = username, firstName
			out = existing
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}

		var referredBy any
		if code := strings.TrimSpace(referrerCode); code != "" {
			var refID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE referral_code = ?`, code).Scan(&refID)
			if err == nil && refID != id {
				referredBy = refID
			}
		}

		code, err := uniqueReferralCode(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, username, first_name, referral_code, referred_by, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`, id, username, firstName, code, referredBy, s.unix()); err != nil {
			return err
		}
		created = true
		out, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
		return err
	})
	return out, created, err
}

const referralAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes: 0/o, 1/l/i

func uniqueReferralCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		var b strings.Builder
		for i := 0; i < 7; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(referralAlphabet))))
			if err != nil {
				return "", err
			}
			b.WriteByte(referralAlphabet[n.Int64()])
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referral_code = ?`, b.String()).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return b.String(), nil
		}
	}
	return "", errors.New("could not generate a unique referral code")
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) SetAgent(ctx context.Context, id int64, agent bool) error {
	return s.exec1(ctx, `UPDATE users SET is_agent = ? WHERE id = ?`, boolInt(agent), id)
}

func (s *Store) SetBlocked(ctx context.Context, id int64, blocked bool) error {
	return s.exec1(ctx, `UPDATE users SET is_blocked = ? WHERE id = ?`, boolInt(blocked), id)
}

// ClaimTrial marks the free trial as used and reports whether THIS call is
// the one that claimed it. The guard in the WHERE clause is what stops a
// customer double-tapping the button into two free accounts.
func (s *Store) ClaimTrial(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET trial_used = 1 WHERE id = ? AND trial_used = 0`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseTrial gives the trial back after the panel refused to create the
// account, so a panel outage never costs a customer their one free trial.
func (s *Store) ReleaseTrial(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET trial_used = 0 WHERE id = ?`, id)
	return err
}

// AdjustBalance changes a balance by delta (positive or negative) and writes
// the matching ledger row, atomically. A change that would make the balance
// negative fails with ErrInsufficientBalance and changes nothing.
func (s *Store) AdjustBalance(ctx context.Context, userID, delta int64, reason string, orderID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		return adjustBalanceTx(ctx, tx, s.unix(), userID, delta, reason, orderID)
	})
}

func adjustBalanceTx(ctx context.Context, tx *sql.Tx, now, userID, delta int64, reason string, orderID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET balance = balance + ? WHERE id = ? AND balance + ? >= 0`, delta, userID, delta)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		return ErrInsufficientBalance
	}
	var order any
	if orderID > 0 {
		order = orderID
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO wallet_ledger (user_id, delta, reason, order_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, delta, reason, order, now)
	return err
}

// RewardReferrer pays the referrer of userID, once per referred user, and
// returns the referrer's id (0 if nothing was paid). The flag flip and the
// credit share one transaction, so a crash in between can neither pay twice
// nor mark it paid without paying.
func (s *Store) RewardReferrer(ctx context.Context, userID, amount, orderID int64) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	var referrer int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(referred_by, 0) FROM users WHERE id = ? AND referral_rewarded = 0`, userID).Scan(&referrer)
		if errors.Is(err, sql.ErrNoRows) || referrer == 0 {
			referrer = 0
			return nil
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET referral_rewarded = 1 WHERE id = ? AND referral_rewarded = 0`, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			referrer = 0
			return nil
		}
		return adjustBalanceTx(ctx, tx, s.unix(), referrer, amount, "referral", orderID)
	})
	return referrer, err
}

func (s *Store) CountReferrals(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referred_by = ?`, userID).Scan(&n)
	return n, err
}

// ListUserIDs returns every non-blocked customer, for broadcasts.
func (s *Store) ListUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users WHERE is_blocked = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) exec1(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
