export const configurationProfiles = {
  incompatible: '此策略配置与当前版本不兼容，无法编辑或复制。',
  invalid: '配置字段不符合当前契约，请检查标记项。',
  missingGroup: '服务端未返回此配置',
  configureGroup: '配置此项',
  unspecified: '未指定',
  defaultValue: '默认',
  specified: '指定',
  adjudicateHint: '默认处理源文残留与标点多余；指定空列表表示不处理任何问题。重复译文不可裁决。',
  chooseCodes: '选择问题代码（可留空）',
  emptyCodes: '指定为空：不处理任何问题',
  codesRequired: '此范围要求至少选择一个问题代码。',
  invalidCodes: '问题代码不属于此轮次支持的范围。',
  ignoredCodes: '此范围不按问题代码筛选；已保存的选择会保留。',
  clearChecks: '清空检查项',
  selectAllChecks: '全选当前检查项',
  checksExplicit: '当前检查项已明确指定；更新省略字段只会保留原值。',
  effect: '保存影响新任务、新预览和即时翻译；已接受任务继续使用原有配置快照。',
} as const

export default configurationProfiles
