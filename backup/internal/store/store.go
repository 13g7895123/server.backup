package store

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 持有資料庫連線池
type Store struct {
	pool *pgxpool.Pool
}

// New 建立 Store，執行 migrations
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("無法連線資料庫: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("資料庫 ping 失敗: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migration 失敗: %w", err)
	}
	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	for _, name := range []string{"001_init.sql", "002_project_details.sql"} {
		path := "/app/migrations/" + name
		sql, err := os.ReadFile(path)
		if err != nil {
			sql, err = os.ReadFile("migrations/" + name)
			if err != nil {
				return fmt.Errorf("無法讀取 migration %s: %w", name, err)
			}
		}
		if _, err = s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("執行 %s 失敗: %w", name, err)
		}
	}
	return nil
}
