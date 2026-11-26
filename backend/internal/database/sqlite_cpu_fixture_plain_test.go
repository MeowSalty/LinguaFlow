//go:build !race

package database

// CPU work outlives the 80ms caller deadline while completing within the
// unchanged five-second native cleanup budget.
const sqliteCPUFixtureSteps = 2_000_000
