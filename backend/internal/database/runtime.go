package database

// Schema hooks move ent's generated default/validator initialization into its
// runtime package. Every production database is opened through this package.
import _ "github.com/MeowSalty/LinguaFlow/backend/internal/ent/runtime"
