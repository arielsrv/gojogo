package main

import "time"

// Order represents a simple order linked to a Customer.
type Order struct {
	CreatedAt  time.Time `json:"created_at"`
	Status     string    `json:"status"      gorm:"size:50;not null"`
	ID         uint      `json:"id"          gorm:"primaryKey"`
	CustomerID uint      `json:"customer_id" gorm:"index;not null"`
	Amount     float64   `json:"amount"      gorm:"not null"`
}
