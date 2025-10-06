package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"gojogo/tracker"
)

type createCustomerRequest struct {
	Name  string  `json:"name"`
	Email string  `json:"email"`
	O1    float64 `json:"o1_amount"`
	O2    float64 `json:"o2_amount"`
}

func main() {
	// Open (or create) a local SQLite DB file via database/sql
	sqlDB, err := sql.Open("sqlite3", "test.db")
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer func(sqlDB *sql.DB) {
		err = sqlDB.Close()
		if err != nil {
			log.Fatalf("failed to close database: %v", err)
		}
	}(sqlDB)

	ctx := context.Background()
	if err = sqlDB.PingContext(ctx); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}

	// Run migrations once at startup using a temporary UoW
	if err = tracker.New(sqlDB).AutoMigrate(&Customer{}, &Order{}); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	// Handlers use a fresh UnitOfWork per request. The shared *sql.DB is safe for concurrent use.
	http.HandleFunc("/customers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createCustomerHandler(sqlDB, w, r)
			return
		}
		// Basic routing for GET /customers/{id}
		if r.Method == http.MethodGet {
			path := strings.TrimPrefix(r.URL.Path, "/customers")
			if path == "" || path == "/" {
				http.Error(w, "use GET /customers/{id}", http.StatusBadRequest)
				return
			}
			getCustomerHandler(sqlDB, w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// Concurrency test: POST /concurrent?n=10
	http.HandleFunc("/concurrent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		concurrentHandler(sqlDB, w, r)
	})

	log.Println("HTTP server listening on :8080")
	if err = http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func createCustomerHandler(sqlDB *sql.DB, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req createCustomerRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // best-effort parse; defaults if empty
	}
	if req.Name == "" {
		req.Name = "Ada Lovelace"
	}
	if req.Email == "" {
		req.Email = fmt.Sprintf("ada+%d@example.com", time.Now().UnixNano())
	}
	if req.O1 == 0 {
		req.O1 = 99.95
	}
	if req.O2 == 0 {
		req.O2 = 149.50
	}

	uow := tracker.New(sqlDB) // new instance per request
	customer := &Customer{Name: req.Name, Email: req.Email}
	uow.Add(customer)

	uow.Do(func(tx tracker.Tx) error {
		o1 := &Order{CustomerID: customer.ID, Amount: req.O1, Status: "NEW"}
		o2 := &Order{CustomerID: customer.ID, Amount: req.O2, Status: "NEW"}
		if err := tx.Create([]*Order{o1, o2}); err != nil {
			return err
		}
		customer.Name = customer.Name + " Jr."
		uow.Update(customer)
		return nil
	})

	// Save all pending work
	if err := uow.SaveChanges(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Load back with orders
	var out Customer
	if err := uow.PreloadFirst(r.Context(), &out, customer.ID, "Orders"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func getCustomerHandler(sqlDB *sql.DB, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "customers" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	idStr := parts[1]
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	uow := tracker.New(sqlDB)
	var out Customer
	if err = uow.PreloadFirst(r.Context(), &out, id, "Orders"); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func concurrentHandler(sqlDB *sql.DB, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	n := 10
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			n = v
		}
	}
	done := make(chan error, n)
	for i := range n {
		go func(i int) {
			u := tracker.New(sqlDB)
			c := &Customer{
				Name:  fmt.Sprintf("User %d", i),
				Email: fmt.Sprintf("user%d+%d@example.com", i, time.Now().UnixNano()),
			}
			u.Add(c)
			u.Do(func(tx tracker.Tx) error {
				return tx.Create(&Order{CustomerID: c.ID, Amount: float64(10 + i), Status: "NEW"})
			})
			done <- u.SaveChanges(r.Context())
		}(i)
	}
	var ok, fail int
	for range n {
		if err := <-done; err != nil {
			fail++
		} else {
			ok++
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"goroutines": n, "ok": ok, "fail": fail})
}
