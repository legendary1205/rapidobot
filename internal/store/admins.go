package store

import "context"

type Admin struct {
	UserID    int64
	AddedBy   int64
	CreatedAt int64
	Username  string // from users, empty if they never opened the bot
	FirstName string
}

// AddAdmin grants admin rights. Adding someone who is already an admin is
// not an error - it reports false so the caller can say so.
func (s *Store) AddAdmin(ctx context.Context, userID, addedBy int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO admins (user_id, added_by, created_at) VALUES (?, ?, ?)`,
		userID, addedBy, s.unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) RemoveAdmin(ctx context.Context, userID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admins WHERE user_id = ?`, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) IsAdmin(ctx context.Context, userID int64) bool {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// ListAdmins returns the admins added from the bot, with their Telegram
// names where known.
func (s *Store) ListAdmins(ctx context.Context) ([]Admin, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.user_id, a.added_by, a.created_at, COALESCE(u.username, ''), COALESCE(u.first_name, '')
		FROM admins a LEFT JOIN users u ON u.id = a.user_id
		ORDER BY a.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(&a.UserID, &a.AddedBy, &a.CreatedAt, &a.Username, &a.FirstName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
