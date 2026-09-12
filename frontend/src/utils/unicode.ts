/**
 * 段落搜索/查找文本的上限，与 API 一致：GET segments 的 search、搜索替换 preview/apply 的 find，
 * 均按 Unicode code point 计（与 JS 的 UTF-16 code unit 不同，代理对算 1）。
 */
export const SEGMENT_SEARCH_MAX_LENGTH = 256

/**
 * 按 Unicode code point 统计文本长度（Array.from 逐码点展开，代理对算 1）；
 * 也作为 NInput 的 count-graphemes 计数传入，使前端输入上限与 API 的码点口径一致。
 */
export const countUnicodeCodePoints = (value: string): number => Array.from(value).length
