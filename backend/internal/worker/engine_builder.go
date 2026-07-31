package worker

// This file is intentionally empty. The engine builder logic has been extracted
// to engine_factory.go as a standalone EngineFactory that is shared between
// JobRunner and PreviewRunner.
//
// - EngineFactory.BuildEngine   replaces JobRunner.buildEngineFromSnapshot
// - BuildEngineConfig           replaces the package-level buildEngineConfig
// - buildTranslateRound etc.    are now standalone functions in engine_factory.go
