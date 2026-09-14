package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ── plans ────────────────────────────────────────────────────────────────────

type Plan struct {
	ID         int64
	Name       string
	DataGB     int64
	Days       int64
	Price      int64
	AgentPrice int64
	IsActive   bool
	Sort       int64
}

// PriceFor is what this customer pays before any discount: the agent price
// for an agent when one is set, the normal price otherwise.
func (p Plan) PriceFor(u User) int64 {
	if u.IsAgent && p.AgentPrice > 0 {
		return p.AgentPrice
	}
	return p.Price
}

const planColumns = `id, name, data_gb, days, price, agent_price, is_active, sort`

func scanPlan(r rowScanner) (Plan, error) {
	var p Plan
	var active int
	err := r.Scan(&p.ID, &p.Name, &p.DataGB, &p.Days, &p.Price, &p.AgentPrice, &active, &p.Sort)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	p.IsActive = active == 1
	return p, err
}

func (s *Store) CreatePlan(ctx context.Context, p Plan) (Plan, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO plans (name, data_gb, days, price, agent_price, is_active, sort, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		p.Name, p.DataGB, p.Days, p.Price, p.AgentPrice, p.Sort, s.unix())
	if err != nil {
		return Plan{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetPlan(ctx, id)
}

func (s *Store) GetPlan(ctx context.Context, id int64) (Plan, error) {
	return scanPlan(s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE id = ?`, id))
}

func (s *Store) ListPlans(ctx context.Context, activeOnly bool) ([]Plan, error) {
	q := `SELECT ` + planColumns + ` FROM plans`
	if activeOnly {
		q += ` WHERE is_active = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort, price, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetPlanActive(ctx context.Context, id int64, active bool) error {
	return s.exec1(ctx, `UPDATE plans SET is_active = ? WHERE id = ?`, boolInt(active), id)
}

// DeletePlan removes a plan nobody has bought. A plan with history is only
// ever deactivated - deleting it would orphan the orders and services that
// point at it.
func (s *Store) DeletePlan(ctx context.Context, id int64) (deleted bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var used int
		if err := tx.QueryRowContext(ctx,
			`SELECT (SELECT COUNT(*) FROM orders WHERE plan_id = ?) + (SELECT COUNT(*) FROM services WHERE plan_id = ?)`,
			id, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			_, err := tx.ExecContext(ctx, `UPDATE plans SET is_active = 0 WHERE id = ?`, id)
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM plans WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		deleted = n == 1
		return nil
	})
	return deleted, err
}

// ── services ────────────────────────────────────────────────────────────────

type Service struct {
	ID            int64
	UserID        int64
	PanelUsername string
	PlanID        int64
	IsTrial       bool
	CreatedAt     int64
}

const serviceColumns = `id, user_id, panel_username, COALESCE(plan_id, 0), is_trial, created_at`

func scanService(r rowScanner) (Service, error) {
	var sv Service
	var trial int
	err := r.Scan(&sv.ID, &sv.UserID, &sv.PanelUsername, &sv.PlanID, &trial, &sv.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrNotFound
	}
	sv.IsTrial = trial == 1
	return sv, err
}

func (s *Store) CreateService(ctx context.Context, userID int64, panelUsername string, planID int64, trial bool) (Service, error) {
	var plan any
	if planID > 0 {
		plan = planID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO services (user_id, panel_username, plan_id, is_trial, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, panelUsername, plan, boolInt(trial), s.unix())
	if err != nil {
		return Service{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetService(ctx, id)
}

func (s *Store) GetService(ctx context.Context, id int64) (Service, error) {
	return scanService(s.db.QueryRowContext(ctx, `SELECT `+serviceColumns+` FROM services WHERE id = ?`, id))
}

func (s *Store) ListServices(ctx context.Context, userID int64) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serviceColumns+` FROM services WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// ── discount codes ──────────────────────────────────────────────────────────

type DiscountCode struct {
	Code      string
	Percent   int64
	Amount    int64
	MaxUses   int64
	Used      int64
	ExpiresAt int64
	IsActive  bool
}

// Apply returns the discount this code gives on price, never more than the
// price itself.
func (d DiscountCode) Apply(price int64) int64 {
	off := d.Amount
	if d.Percent > 0 {
		off = price * d.Percent / 100
	}
	if off > price {
		off = price
	}
	if off < 0 {
		off = 0
	}
	return off
}

const discountColumns = `code, percent, amount, max_uses, used, expires_at, is_active`

func scanDiscount(r rowScanner) (DiscountCode, error) {
	var d DiscountCode
	var active int
	err := r.Scan(&d.Code, &d.Percent, &d.Amount, &d.MaxUses, &d.Used, &d.ExpiresAt, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return DiscountCode{}, ErrNotFound
	}
	d.IsActive = active == 1
	return d, err
}

func (s *Store) CreateDiscount(ctx context.Context, d DiscountCode) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO discount_codes (code, percent, amount, max_uses, expires_at, is_active, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?)`,
		strings.TrimSpace(d.Code), d.Percent, d.Amount, d.MaxUses, d.ExpiresAt, s.unix())
	return err
}

func (s *Store) GetDiscount(ctx context.Context, code string) (DiscountCode, error) {
	return scanDiscount(s.db.QueryRowContext(ctx,
		`SELECT `+discountColumns+` FROM discount_codes WHERE code = ?`, strings.TrimSpace(code)))
}

func (s *Store) ListDiscounts(ctx context.Context) ([]DiscountCode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+discountColumns+` FROM discount_codes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscountCode
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SetDiscountActive(ctx context.Context, code string, active bool) error {
	return s.exec1(ctx, `UPDATE discount_codes SET is_active = ? WHERE code = ?`, boolInt(active), code)
}

// DiscountUsedBy reports whether this customer already spent the code.
func (s *Store) DiscountUsedBy(ctx context.Context, code string, userID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM discount_uses WHERE code = ? AND user_id = ?`, code, userID).Scan(&n)
	return n > 0, err
}

// ── settings ────────────────────────────────────────────────────────────────

// Setting returns a stored setting, or def when it has never been set.
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
