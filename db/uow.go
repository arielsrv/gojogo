package db

import (
	"context"
	"sync"

	"gorm.io/gorm"
)

// Operation represents a deferred operation to be executed inside the transaction.
// It receives the transactional gorm.DB and should return an error to rollback the transaction.
type Operation func(tx *gorm.DB) error

// UnitOfWork implements a simple Unit of Work pattern on top of GORM.
// It collects changes and applies them in a single transaction on SaveChanges/SaveChanges.
// It also provides a basic object tracker, similar in spirit to EF's ChangeTracker,
// allowing you to queue Add/Update/Delete operations for entities, in addition to
// arbitrary functions via Do.
type UnitOfWork struct {
	root *gorm.DB

	mu       sync.Mutex
	ops      []Operation
	toCreate []any
	toUpdate []any
	toDelete []any

	// afterCommit contains callbacks to run after a successful commit (outside tx)
	afterCommit []func()
	// afterRollback contains callbacks to run after a rollback (outside tx)
	afterRollback []func()
}

// New creates a new UnitOfWork using the provided gorm.DB as the root connection.
// The root DB must be configured and open. It will not start a transaction until SaveChanges/SaveChanges.
func New(db *gorm.DB) *UnitOfWork {
	return &UnitOfWork{root: db}
}

// Root returns the underlying root *gorm.DB.
func (u *UnitOfWork) Root() *gorm.DB { return u.root }

// Do queue a custom operation to be executed inside the transaction at commit time.
func (u *UnitOfWork) Do(op Operation) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ops = append(u.ops, op)
}

// Add tracks an entity to be created on commit.
func (u *UnitOfWork) Add(entity any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.toCreate = append(u.toCreate, entity)
}

// Update tracks an entity to be updated on commit.
func (u *UnitOfWork) Update(entity any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.toUpdate = append(u.toUpdate, entity)
}

// RegisterDelete tracks an entity to be deleted on commit.
func (u *UnitOfWork) RegisterDelete(entity any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.toDelete = append(u.toDelete, entity)
}

// AfterCommit registers a callback to be executed after a successful commit (outside transaction).
func (u *UnitOfWork) AfterCommit(cb func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.afterCommit = append(u.afterCommit, cb)
}

// AfterRollback registers a callback to be executed after a rollback (outside transaction).
func (u *UnitOfWork) AfterRollback(cb func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.afterRollback = append(u.afterRollback, cb)
}

// SaveChanges commits all tracked changes in a single transaction.
// It is an alias for SaveChanges to resemble EF's SaveChanges terminology.
func (u *UnitOfWork) SaveChanges(ctx context.Context) error { return u.Commit(ctx) }

// Commit begins a transaction and applies all pending operations.
// On error, the transaction is rolled back and the pending operations remain queued
// so the caller can inspect or retry if desired. Use Clear() to discard them.
func (u *UnitOfWork) Commit(ctx context.Context) error {
	u.mu.Lock()
	deferredOps := make([]Operation, len(u.ops))
	copy(deferredOps, u.ops)
	creates := append([]any(nil), u.toCreate...)
	updates := append([]any(nil), u.toUpdate...)
	deletes := append([]any(nil), u.toDelete...)
	afterCommit := append([]func(){}, u.afterCommit...)
	afterRollback := append([]func(){}, u.afterRollback...)
	u.mu.Unlock()

	txErr := u.root.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			if err := op(tx); err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		for _, cb := range afterRollback {
			// best-effort and safe, do not shadow txErr if callback fails
			func() { defer func() { _ = recover() }(); cb() }()
		}
		return txErr
	}

	// On success, clear pending items and run after-commit callbacks
	u.Clear()
	for _, cb := range afterCommit {
		func() { defer func() { _ = recover() }(); cb() }()
	}
	return nil
}

// Clear discards all pending operations and tracked entities.
func (u *UnitOfWork) Clear() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ops = nil
	u.toCreate = nil
	u.toUpdate = nil
	u.toDelete = nil
	u.afterCommit = nil
	u.afterRollback = nil
}

// HasPending returns true if there are any queued operations or tracked changes.
func (u *UnitOfWork) HasPending() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.ops) > 0 || len(u.toCreate) > 0 || len(u.toUpdate) > 0 || len(u.toDelete) > 0
}
