package main

import (
	"context"
	"fmt"
	"gojogo/db"
	"os"

	"log"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Customer represents a customer with one-to-many Orders
type Customer struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:200;not null"`
	Email     string `gorm:"size:255;uniqueIndex"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Orders []Order
}

// Order represents a simple order linked to a Customer
type Order struct {
	ID         uint    `gorm:"primaryKey"`
	CustomerID uint    `gorm:"index;not null"`
	Amount     float64 `gorm:"not null"`
	Status     string  `gorm:"size:50;not null"`
	CreatedAt  time.Time
}

func main() {
	// Open (or create) a local SQLite DB file
	gormDb, err := gorm.Open(sqlite.Open("test.db"), &gorm.Config{
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
			logger.Config{
				SlowThreshold:             time.Second, // Slow SQL threshold
				LogLevel:                  logger.Info, // Log level
				IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
				ParameterizedQueries:      true,        // Don't include params in the SQL log
				Colorful:                  false,       // Disable color
			}),
	})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	// Auto-migrate our entities
	if err = gormDb.AutoMigrate(&Customer{}, &Order{}); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	// Add a UnitOfWork instance
	uow := db.New(gormDb)

	ctx := context.Background()

	// Prepare a new customer to be created
	customer := &Customer{Name: "Ada Lovelace", Email: fmt.Sprintf("ada+%d@example.com", time.Now().Unix())}
	uow.Add(customer)

	// After we create the customer (during commit), we can create a couple of orders
	uow.Do(func(tx *gorm.DB) error {
		// At this point, customer.ID is already populated by the earlier Add in the same transaction
		o1 := &Order{CustomerID: customer.ID, Amount: 99.95, Status: "NEW"}
		o2 := &Order{CustomerID: customer.ID, Amount: 149.50, Status: "NEW"}
		if err = tx.Create([]*Order{o1, o2}).Error; err != nil {
			return err
		}
		// Demonstrate an update queued as well (optional): change the customer name
		customer.Name = customer.Name + " Jr."
		// We queue an update so it's applied after the creations
		uow.Update(customer)
		return nil
	})

	// Hook after commit (executed outside the transaction)
	uow.AfterCommit(func() {
		log.Println("Transaction committed successfully")
	})

	// SaveChanges all the pending work
	if err = uow.SaveChanges(ctx); err != nil {
		log.Fatalf("commit failed: %v", err)
	}

	// SaveChanges all the pending work
	if err = uow.SaveChanges(ctx); err != nil {
		log.Fatalf("commit failed: %v", err)
	}

	// Read back and print what we have
	var out Customer
	if err = gormDb.Preload("Orders").First(&out, customer.ID).Error; err != nil {
		log.Fatalf("failed to load customer: %v", err)
	}

	fmt.Printf("Customer: #%d %s <%s>\n", out.ID, out.Name, out.Email)
	for _, o := range out.Orders {
		fmt.Printf("  Order #%d Amount=%.2f Status=%s\n", o.ID, o.Amount, o.Status)
	}

	fmt.Println("Done.")
}
