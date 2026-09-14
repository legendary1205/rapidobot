package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type OrderKind string

const (
	KindPurchase OrderKind = "purchase"
	KindRenew    OrderKind = "renew"
	KindTopup    OrderKind = "topup"
)

type OrderStatus string

const (
	StatusDraft           OrderStatus = "draft"
	StatusAwaitingReceipt OrderStatus = "awaiting_receipt"
	StatusAwaitingReview  OrderStatus = "awaiting_review"
	StatusProcessing      OrderStatus = "processing"
	StatusCompleted       OrderStatus = "completed"
	StatusRejected        OrderStatus = "rejected"
	StatusFailed          OrderStatus = "failed"
	StatusCancelled       OrderStatus = "cancelled"
)

type Order struct {
	ID            int64
	UserID        int64
	Kind          OrderKind
	PlanID        int64
	ServiceID     int64
	BaseAmount    int64
	Discount      int64
	Amount        int64
	DiscountCode  string
	PayMethod     string
	Status        OrderStatus
	ReceiptFileID string
	DecidedBy     int64
	Error         string
	CreatedAt     int64
	UpdatedAt     int64
}

const orderColumns = `id, user_id, kind, COALESCE(plan_id, 0), COALESCE(service_id, 0), base_amount, discount,
	amount, COALESCE(discount_code, ''), COALESCE(pay_method, ''), status, COALESCE(receipt_file_id, ''),
	COALESCE(decided_by, 0), COALESCE(error, ''), created_at, updated_at`

func scanOrder(r rowScanner) (Order, error) {
	var o Order
	var kind, status string
	err := r.Scan(&o.ID, &o.UserID, &kind, &o.PlanID, &o.ServiceID, &o.BaseAmount, &o.Discount,
		&o.Amount, &o.DiscountCode, &o.PayMethod, &status, &o.ReceiptFileID,
		&o.DecidedBy, &o.Error, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	o.Kind, o.Status = OrderKind(kind), OrderStatus(status)
	return o, err
}

func nullID(id int64) any {
	if id > 0 {
		return id
	}
	return nil
}

// CreateOrder starts a new order as a draft.
func (s *Store) CreateOrder(ctx context.Context, o Order) (Order, error) {
	now := s.unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO orders (user_id, kind, plan_id, service_id, base_amount, discount, amount, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, 'draft', ?, ?)`,
		o.UserID, string(o.Kind), nullID(o.PlanID), nullID(o.ServiceID), o.BaseAmount, o.BaseAmount, now, now)
	if err != nil {
		return Order{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetOrder(ctx, id)
}

func (s *Store) GetOrder(ctx context.Context, id int64) (Order, error) {
	return scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = ?`, id))
}

// ApplyDiscount sets a discount on a draft order the customer owns. The
// amount is always recomputed from base_amount, so applying a second code
// replaces the first instead of stacking on top of it.
func (s *Store) ApplyDiscount(ctx context.Context, orderID, userID int64, code string, discount int64) (Order, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET discount = ?, amount = base_amount - ?, discount_code = ?, updated_at = ?
		 WHERE id = ? AND user_id = ? AND status = 'draft' AND ? <= base_amount`,
		discount, discount, code, s.unix(), orderID, userID, discount)
	if err != nil {
		return Order{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Order{}, ErrConflict
	}
	return s.GetOrder(ctx, orderID)
}

// Transition moves an order from one of `from` to `to`, and fails with
// ErrConflict if it was not in any of them. Every state change that decides
// money goes through here: when two admins tap "approve" on the same
// receipt, exactly one UPDATE matches and the other gets ErrConflict.
func (s *Store) Transition(ctx context.Context, orderID int64, from []OrderStatus, to OrderStatus, decidedBy int64) error {
	if len(from) == 0 {
		return errors.New("transition needs at least one source status")
	}
	args := []any{string(to), nullID(decidedBy), s.unix(), orderID}
	marks := make([]string, len(from))
	for i, st := range from {
		marks[i] = "?"
		args = append(args, string(st))
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = ?, decided_by = COALESCE(?, decided_by), updated_at = ?
		 WHERE id = ? AND status IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

// ChooseCard switches a draft to card payment and waits for the receipt.
func (s *Store) ChooseCard(ctx context.Context, orderID, userID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET pay_method = 'card', status = 'awaiting_receipt', updated_at = ?
		 WHERE id = ? AND user_id = ? AND status IN ('draft', 'awaiting_receipt')`, s.unix(), orderID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

// AttachReceipt stores the receipt photo on the customer's open card order
// and sends it to review. It returns the order, or ErrNotFound when the
// customer has no order waiting for a receipt.
func (s *Store) AttachReceipt(ctx context.Context, userID int64, fileID string) (Order, error) {
	var out Order
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		o, err := scanOrder(tx.QueryRowContext(ctx,
			`SELECT `+orderColumns+` FROM orders WHERE user_id = ? AND status = 'awaiting_receipt'
			 ORDER BY id DESC LIMIT 1`, userID))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET receipt_file_id = ?, status = 'awaiting_review', updated_at = ? WHERE id = ?`,
			fileID, s.unix(), o.ID); err != nil {
			return err
		}
		o.ReceiptFileID, o.Status = fileID, StatusAwaitingReview
		out = o
		return nil
	})
	return out, err
}

// PayFromWallet debits the customer and moves the draft to processing in
// one transaction - either both happen or neither does. The balance CHECK
// constraint and the guarded UPDATE together make overspending impossible
// even if the customer taps "pay" twice at once.
func (s *Store) PayFromWallet(ctx context.Context, orderID, userID int64) (Order, error) {
	var out Order
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		o, err := scanOrder(tx.QueryRowContext(ctx,
			`SELECT `+orderColumns+` FROM orders WHERE id = ? AND user_id = ?`, orderID, userID))
		if err != nil {
			return err
		}
		if o.Status != StatusDraft {
			return ErrConflict
		}
		if o.Kind == KindTopup {
			return errors.New("a top-up cannot be paid from the wallet")
		}
		if err := adjustBalanceTx(ctx, tx, s.unix(), userID, -o.Amount, string(o.Kind), o.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET pay_method = 'wallet', status = 'processing', updated_at = ? WHERE id = ?`,
			s.unix(), o.ID); err != nil {
			return err
		}
		o.PayMethod, o.Status = "wallet", StatusProcessing
		out = o
		return nil
	})
	return out, err
}

// CompleteOrder finishes a processing order. For a discounted order it also
// spends the code - here, and not when the code was typed, so a receipt that
// gets rejected never burns the customer's one use of it.
func (s *Store) CompleteOrder(ctx context.Context, orderID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = ?`, orderID))
		if err != nil {
			return err
		}
		if o.Status != StatusProcessing {
			return ErrConflict
		}
		if o.DiscountCode != "" {
			res, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO discount_uses (code, user_id, order_id, used_at) VALUES (?, ?, ?, ?)`,
				o.DiscountCode, o.UserID, o.ID, s.unix())
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				if _, err := tx.ExecContext(ctx,
					`UPDATE discount_codes SET used = used + 1 WHERE code = ?`, o.DiscountCode); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE orders SET status = 'completed', error = NULL, updated_at = ? WHERE id = ?`, s.unix(), o.ID)
		return err
	})
}

// CompleteTopup credits the wallet and closes the top-up order in ONE
// transaction. Done as two steps, a crash between them would leave an order
// stuck in processing with the money already credited - and nothing could
// safely retry it, because approving again is correctly refused.
func (s *Store) CompleteTopup(ctx context.Context, orderID int64) (Order, error) {
	var out Order
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = ?`, orderID))
		if err != nil {
			return err
		}
		if o.Status != StatusProcessing || o.Kind != KindTopup {
			return ErrConflict
		}
		if err := adjustBalanceTx(ctx, tx, s.unix(), o.UserID, o.Amount, "topup", o.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET status = 'completed', updated_at = ? WHERE id = ?`, s.unix(), o.ID); err != nil {
			return err
		}
		o.Status = StatusCompleted
		out = o
		return nil
	})
	return out, err
}

// FailOrder marks a processing order failed and, if it was paid from the
// wallet, refunds it in the same transaction. A delivery that failed never
// keeps the customer's money.
func (s *Store) FailOrder(ctx context.Context, orderID int64, reason string) (refunded bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = ?`, orderID))
		if err != nil {
			return err
		}
		if o.Status != StatusProcessing {
			return ErrConflict
		}
		if o.PayMethod == "wallet" && o.Amount > 0 {
			if err := adjustBalanceTx(ctx, tx, s.unix(), o.UserID, o.Amount, "refund", o.ID); err != nil {
				return err
			}
			refunded = true
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE orders SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`, reason, s.unix(), o.ID)
		return err
	})
	return refunded, err
}

// CancelOpenOrders closes a customer's older unpaid orders when they start
// a new one, so an abandoned checkout can never have a receipt attached to
// it by mistake later.
func (s *Store) CancelOpenOrders(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = 'cancelled', updated_at = ?
		 WHERE user_id = ? AND status IN ('draft', 'awaiting_receipt')`, s.unix(), userID)
	return err
}

func (s *Store) ListOrdersByStatus(ctx context.Context, status OrderStatus, limit int) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE status = ? ORDER BY id LIMIT ?`, string(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type Stats struct {
	Users          int64
	Agents         int64
	Services       int64
	CompletedSales int64
	Revenue        int64
	RevenueToday   int64
	PendingReview  int64
}

func (s *Store) Stats(ctx context.Context, startOfToday int64) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(*) FROM users WHERE is_agent = 1),
		(SELECT COUNT(*) FROM services WHERE is_trial = 0),
		(SELECT COUNT(*) FROM orders WHERE status = 'completed' AND kind != 'topup'),
		(SELECT COALESCE(SUM(amount), 0) FROM orders WHERE status = 'completed' AND pay_method = 'card'),
		(SELECT COALESCE(SUM(amount), 0) FROM orders WHERE status = 'completed' AND pay_method = 'card' AND updated_at >= ?),
		(SELECT COUNT(*) FROM orders WHERE status = 'awaiting_review')`, startOfToday).Scan(
		&st.Users, &st.Agents, &st.Services, &st.CompletedSales, &st.Revenue, &st.RevenueToday, &st.PendingReview)
	return st, err
}
