package storagemigrate

import "github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"

func ReadOnlyDSN(driver, dsn string) (string, error) { return snapshot.ReadOnlyDSN(driver, dsn) }
