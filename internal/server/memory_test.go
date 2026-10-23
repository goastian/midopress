package server_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/goastian/midopress/internal/server"
	"github.com/goastian/midopress/internal/sysmem"
)

func TestMemoryBudgetIsAutomaticWhenZero(t *testing.T) {
	assert.Equal(t, int64(512<<20), server.MemoryBudget(512<<20), "what is configured wins")
	auto := server.MemoryBudget(0)
	assert.GreaterOrEqual(t, auto, int64(256<<20))
	assert.LessOrEqual(t, auto, int64(8<<30))
	if total := sysmem.Total(); total > 0 {
		assert.Equal(t, sysmem.Budget(total, 256<<20, 8<<30), auto)
	}
}
