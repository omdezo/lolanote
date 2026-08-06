package memory_test

import (
	"testing"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/repository/repotest"
)

// The in-memory repo stands in for Mongo across most of the service tests. Any
// behaviour it does not share is a hole those tests cannot see through — so it
// is held to the same contract.
func TestElementRepo_Contract(t *testing.T) {
	repotest.RunElementRepositoryContract(t, func(t *testing.T) domain.ElementRepository {
		return memory.NewElementRepo()
	})
}
