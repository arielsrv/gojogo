package tracker

import (
	"context"
	"database/sql"
	"sync"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Tx is a minimal transaction interface that hides GORM from callers.
// It offers basic data operations used by this project.
type Tx interface {
	Create(value any) error
	Save(value any) error
	Delete(value any, conds ...any) error
}

type gormTx struct{ db *gorm.DB }

func (r gormTx) Create(value any) error               { return r.db.Create(value).Error }
func (r gormTx) Save(value any) error                 { return r.db.Save(value).Error }
func (r gormTx) Delete(value any, conds ...any) error { return r.db.Delete(value, conds...).Error }

// Operation represents a deferred operation to be executed inside the transaction.
// It receives an abstract Tx to avoid leaking GORM to the outside world.
type Operation func(tx Tx) error

// UnitOfWork implements a simple Unit of Work pattern on top of GORM.
// It collects changes and applies them in a single transaction on SaveChanges/SaveChanges.
// It also provides a basic object tracker, similar in spirit to EF's ChangeTracker,
// allowing you to queue Add/Update/Delete operations for entities, in addition to
// arbitrary functions via Do.
type UnitOfWork struct {
	root *gorm.DB

	ops      []Operation
	toCreate []any
	toUpdate []any
	toDelete []any

	// afterCommit contains callbacks to run after a successful commit (outside tx)
	afterCommit []func()
	// afterRollback contains callbacks to run after a rollback (outside tx)
	afterRollback []func()

	mu sync.Mutex
}

// gormRoots caches a single *gorm.DB per *sql.DB so we don't call gorm.Open on every tracker.New.
// This keeps the public API simple while avoiding repeated initialization cost.
// Note: entries are not pruned automatically; ensure you reuse *sql.DB for app lifetime.
var gormRoots sync.Map

// New creates a new UnitOfWork using the provided standard sql.DB as the root connection.
// Internally, it uses GORM with the SQLite driver, but callers don't need to know that.
func New(sqlDB *sql.DB) *UnitOfWork {
	if v, ok := gormRoots.Load(sqlDB); ok {
		return &UnitOfWork{root: v.(*gorm.DB)}
	}
	gdb, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{})
	if err == nil && gdb != nil {
		actual, _ := gormRoots.LoadOrStore(sqlDB, gdb)
		return &UnitOfWork{root: actual.(*gorm.DB)}
	}
	// Fallback preserves previous behavior of ignoring open errors, but root may be nil.
	return &UnitOfWork{root: gdb}
}

// AutoMigrate runs auto-migrations for the given models without exposing GORM.
func (r *UnitOfWork) AutoMigrate(models ...any) error { return r.root.AutoMigrate(models...) }

// Do queue a custom operation to be executed inside the transaction at commit time.
func (r *UnitOfWork) Do(op Operation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

// Add tracks an entity to be created on commit.
func (r *UnitOfWork) Add(entity any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toCreate = append(r.toCreate, entity)
}

// Update tracks an entity to be updated on commit.
func (r *UnitOfWork) Update(entity any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toUpdate = append(r.toUpdate, entity)
}

// RegisterDelete tracks an entity to be deleted on commit.
func (r *UnitOfWork) RegisterDelete(entity any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toDelete = append(r.toDelete, entity)
}

// AfterCommit registers a callback to be executed after a successful commit (outside transaction).
func (r *UnitOfWork) AfterCommit(cb func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterCommit = append(r.afterCommit, cb)
}

// AfterRollback registers a callback to be executed after a rollback (outside transaction).
func (r *UnitOfWork) AfterRollback(cb func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterRollback = append(r.afterRollback, cb)
}

// SaveChanges commits all tracked changes in a single transaction.
// It is an alias for SaveChanges to resemble EF's SaveChanges terminology.
func (r *UnitOfWork) SaveChanges(ctx context.Context) error { return r.Commit(ctx) }

// Commit begins a transaction and applies all pending operations.
// On error, the transaction is rolled back and the pending operations remain queued
// so the caller can inspect or retry if desired. Use Clear() to discard them.
func (r *UnitOfWork) Commit(ctx context.Context) error {
	r.mu.Lock()
	deferredOps := make([]Operation, len(r.ops))
	copy(deferredOps, r.ops)
	creates := append([]any(nil), r.toCreate...)
	updates := append([]any(nil), r.toUpdate...)
	deletes := append([]any(nil), r.toDelete...)
	afterCommit := append([]func(){}, r.afterCommit...)
	afterRollback := append([]func(){}, r.afterRollback...)
	r.mu.Unlock()

	txErr := r.root.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Apply creates
		for _, e := range creates {
			if err := tx.Create(e).Error; err != nil {
				return err
			}
		}
		// 2. Apply updates
		for _, e := range updates {
			if err := tx.Save(e).Error; err != nil { // Save handles both insert/update by PK, but we used Add above for clarity
				return err
			}
		}
		// 3. Apply deletes
		for _, e := range deletes {
			if err := tx.Delete(e).Error; err != nil {
				return err
			}
		}
		// 4. Apply custom operations
		for _, op := range deferredOps {
			if err := op(gormTx{db: tx}); err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		for _, cb := range afterRollback {
			// best-effort and safe do not shadow txErr if callback fails
			func() { defer func() { _ = recover() }(); cb() }()
		}
		return txErr
	}

	// On success, clear pending items and run after-commit callbacks
	r.Clear()
	for _, cb := range afterCommit {
		func() { defer func() { _ = recover() }(); cb() }()
	}
	return nil
}

// Clear discards all pending operations and tracked entities.
func (r *UnitOfWork) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = nil
	r.toCreate = nil
	r.toUpdate = nil
	r.toDelete = nil
	r.afterCommit = nil
	r.afterRollback = nil
}

// HasPending returns true if there are any queued operations or tracked changes.
func (r *UnitOfWork) HasPending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ops) > 0 || len(r.toCreate) > 0 || len(r.toUpdate) > 0 || len(r.toDelete) > 0
}

// First fetches the first record that matches the conditions into out, without exposing GORM.
func (r *UnitOfWork) First(ctx context.Context, out any, conds ...any) error {
	return r.root.WithContext(ctx).First(out, conds...).Error
}

// PreloadFirst preloads associations and fetches the first record by primary key.
func (r *UnitOfWork) PreloadFirst(ctx context.Context, out any, id any, preloads ...string) error {
	db := r.root.WithContext(ctx)
	for _, p := range preloads {
		db = db.Preload(p)
	}
	return db.First(out, id).Error
}
