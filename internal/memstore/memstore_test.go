package memstore_test

import (
	"testing"

	"github.com/adamsjoe/results-service/internal/memstore"
	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) service.Store { return memstore.New() })
}
