package cli

import "github.com/MeowSalty/LinguaFlow/backend/internal/config"

func testServerConfig() *config.ServerConfig {
	return config.DefaultServerConfig()
}

func resolveTestServerConfig(in config.ServerInputs) (*config.ResolvedServer, error) {
	return config.ResolveServerConfig(in)
}
