package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresErrorClassification(t *testing.T) {
	cases := []struct {
		err         error
		unavailable bool
	}{
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{context.DeadlineExceeded, true},
		{&pgconn.PgError{Code: "08006"}, true},
		{&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "42601"}, false},
		{&pgconn.PgError{Code: "23505"}, false},
		{context.Canceled, false},
		{errors.New("unexpected"), false},
	}
	for _, tc := range cases {
		got := postgresError(fmt.Errorf("wrapped: %w", tc.err))
		if errors.Is(got, ErrUnavailable) != tc.unavailable || !errors.Is(got, tc.err) {
			t.Errorf("%v: %v", tc.err, got)
		}
	}
}
