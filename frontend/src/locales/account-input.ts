/** Shared account input validation messages. */
export const accountInputMessages = {
  username: {
    input: '仅支持 ASCII 字符，且原始长度不超过 128 字节。',
    format:
      '用户名需 3–32 个字符，以字母开头、以字母或数字结尾，仅可使用小写字母、数字及不相邻的 -、_。',
    reserved: '该用户名是保留名，不能用于创建账号。',
  },
  email: {
    optionalHint: '选填；填写后仅作为未验证邮箱资料保存，不作为唯一登录归属。',
    requiredHint: '必填；作为未验证的注册邮箱资料保存，不授予登录或找回权限。',
    required: '请输入注册邮箱。',
    invalid: '请输入单个有效邮箱地址，不支持显示名称或多个邮箱。',
  },
} as const
