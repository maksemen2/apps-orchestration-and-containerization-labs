package store

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnavailable = errors.New("database unavailable")

type Order struct {
	ID          int64      `json:"id"`
	Item        string     `json:"item"`
	Quantity    int        `json:"quantity"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
}

type Store struct {
	pool  *pgxpool.Pool
	ready atomic.Bool
}

func New(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	config.MaxConns = 5
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("cannot configure database pool")
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()      { s.pool.Close() }
func (s *Store) Ready() bool { return s.ready.Load() }

// Monitor initializes the small lab schema and keeps dependency readiness current.
// The lock serializes initialization when several replicas start together.
func (s *Store) Monitor(ctx context.Context, logger *slog.Logger, onReady func(bool)) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var err error
		if s.Ready() {
			err = s.pool.Ping(checkCtx)
		} else {
			err = s.ensureSchema(checkCtx)
		}
		cancel()
		ready := err == nil
		if ready != s.ready.Swap(ready) {
			onReady(ready)
			if ready {
				logger.Info("database ready")
			} else {
				logger.Warn("database unavailable")
			}
		} else if err != nil {
			logger.Warn("database retry failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(307829831)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS orders (
		id BIGSERIAL PRIMARY KEY,
		item TEXT NOT NULL,
		quantity INTEGER NOT NULL CHECK (quantity > 0),
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processed')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		processed_at TIMESTAMPTZ
	)`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Create(ctx context.Context, item string, quantity int) (Order, error) {
	if !s.Ready() {
		return Order{}, ErrUnavailable
	}
	var o Order
	err := s.pool.QueryRow(ctx, `INSERT INTO orders (item, quantity) VALUES ($1, $2)
		RETURNING id, item, quantity, status, created_at, processed_at`, item, quantity).
		Scan(&o.ID, &o.Item, &o.Quantity, &o.Status, &o.CreatedAt, &o.ProcessedAt)
	return o, err
}

func (s *Store) List(ctx context.Context) ([]Order, error) {
	if !s.Ready() {
		return nil, ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, `SELECT id, item, quantity, status, created_at, processed_at FROM orders ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]Order, 0)
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.Item, &o.Quantity, &o.Status, &o.CreatedAt, &o.ProcessedAt); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return orders, nil
}

// ProcessOne claims a row and changes its state in one statement. SKIP LOCKED
// lets other replicas claim different rows without double processing.
func (s *Store) ProcessOne(ctx context.Context) (int64, bool, error) {
	if !s.Ready() {
		return 0, false, ErrUnavailable
	}
	var id int64
	err := s.pool.QueryRow(ctx, `WITH next_order AS (
		SELECT id FROM orders WHERE status = 'pending' ORDER BY id
		FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE orders SET status = 'processed', processed_at = now()
	WHERE id = (SELECT id FROM next_order) RETURNING id`).Scan(&id)
	if err != nil {
		// pgx.ErrNoRows means the queue is empty, not a database failure.
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}
