//go:build integration

package store_test

import (
	"context"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// startPostgres starts a throwaway Postgres container for the test run and
// returns its connection URL and a function that removes it.
func startPostgres(ctx context.Context) (string, func(), error) {
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", nil, err
	}
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return "", nil, err
	}
	return url, stop, nil
}
