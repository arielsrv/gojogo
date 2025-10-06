package main

import "time"

// Customer represents a customer with one-to-many Orders
// JSON tags for HTTP responses.
type Customer struct {
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Name  string `gorm:"size:200;not null"    json:"name"`
	Email string `gorm:"size:255;uniqueIndex" json:"email"`

	Orders []Order `json:"orders,omitempty"`
	ID     uint    `json:"id"               gorm:"primaryKey"`
}
