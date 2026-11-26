//go:build race

package database

// Race instrumentation adds substantial cost to modernc's translated SQLite
// VM. Scale only this fixture's work; caller and native cleanup budgets remain
// identical to the ordinary test.
const sqliteCPUFixtureSteps = 200_000
