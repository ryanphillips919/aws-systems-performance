package store

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("link not found")

type Link struct {
	Code           string    `json:"code"`
	UserID         string    `json:"userId"`
	DestinationURL string    `json:"destinationUrl"`
	CreatedAt      time.Time `json:"createdAt"`
	ClickCount     int64     `json:"clickCount"`
}

// Store describes the application's current operations. Backends may have
// different consistency and efficiency characteristics even with the same API.
type Store interface {
	Create(context.Context, string, string, string) (Link, error)
	Get(context.Context, string) (Link, error)
	List(context.Context, string, int) ([]Link, error)
	Click(context.Context, string) (Link, error)
	Popular(context.Context, int) ([]Link, error)
	Health(context.Context) error
}
