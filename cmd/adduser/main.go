// Command adduser creates a user directly in the database. It exists to
// bootstrap the first admin, since POST /users itself requires an admin.
//
//	go run ./cmd/adduser -email admin@example.com -name Admin -role admin -password 'change-me-please'
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var in service.CreateUserInput
	var role string
	flag.StringVar(&in.Email, "email", "", "user email")
	flag.StringVar(&in.Name, "name", "", "display name")
	flag.StringVar(&in.Password, "password", "", "password (8-72 bytes)")
	flag.StringVar(&role, "role", string(auth.RoleViewer), "admin, manager or viewer")
	flag.Parse()
	in.Role = auth.Role(role)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Creating a user does not issue tokens, so no TokenIssuer is needed.
	u, err := service.NewUserService(store.New(pool), nil).Create(ctx, in)
	if err != nil {
		return err
	}
	fmt.Printf("created user id=%d email=%s role=%s\n", u.ID, u.Email, u.Role)
	return nil
}
