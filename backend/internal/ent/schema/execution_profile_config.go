package schema

import "github.com/MeowSalty/LinguaFlow/backend/internal/execution"

type ExecutionProfileConfigData = execution.ProfileSpec
type ProfileProtectConfig = execution.ProfileProtectConfig
type ProfileRubyConfig = execution.ProfileRubyConfig
type ProfilePostprocessConfig = execution.ProfilePostprocessConfig
type ProfileRepairConfig = execution.ProfileRepairConfig
type ProfileGlossaryConfig = execution.ProfileGlossaryConfig
type ProfileBootstrapConfig = execution.ProfileBootstrapConfig
type ProfileContextConfig = execution.ProfileContextConfig
type ProfileQAConfig = execution.ProfileQAConfig

func DefaultProfileConfig() ExecutionProfileConfigData { return execution.DefaultProfile() }
