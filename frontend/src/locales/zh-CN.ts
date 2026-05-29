const messages = {
  common: {
    appName: 'LinguaFlow',
    language: '语言',
    loadMore: '加载更多',
    switchService: '切换服务器',
    currentService: '当前服务器',
  },
  nav: {
    main: '主导航',
    dashboard: '工作台',
    about: '关于',
  },
  layout: {
    userMenu: {
      switchService: '切换服务器',
      logout: '退出登录',
    },
    messages: {
      logoutSuccess: '已退出登录',
      logoutFailed: '退出登录失败，请重试',
    },
  },
  locale: {
    zhCN: '简体中文',
  },
  service: {
    title: '选择 LinguaFlow 服务器',
    subtitle: '填写你要连接的后端 API 地址，可以是自部署实例或托管服务',
    form: {
      baseUrl: '服务器地址',
      baseUrlPlaceholder: 'https://linguaflow.example.com/api/v1',
      submit: '连接',
    },
    validation: {
      required: '请填写服务器地址',
      invalidUrl: '请填写合法的 URL，例如 https://linguaflow.example.com/api/v1',
    },
    messages: {
      connected: '已连接到 {url}',
    },
    hints: {
      prefix: '留空或填写',
      suffix: '将使用当前页面的同源地址',
    },
  },
  login: {
    title: '登录',
    subtitle: '使用账号登录 LinguaFlow',
    form: {
      username: '用户名',
      usernamePlaceholder: '请输入用户名',
      password: '密码',
      passwordPlaceholder: '请输入密码',
      submit: '登录',
    },
    validation: {
      usernameRequired: '请输入用户名',
      passwordRequired: '请输入密码',
    },
    messages: {
      success: '登录成功',
      failed: '登录失败，请检查用户名和密码',
    },
    links: {
      switchService: '切换服务器 · {url}',
      register: '没有账号？去注册',
    },
  },
  register: {
    title: '注册账号',
    subtitle: '创建一个 LinguaFlow 账号',
    form: {
      username: '用户名',
      usernamePlaceholder: '3-32 位，字母 / 数字 / 下划线',
      email: '邮箱',
      emailPlaceholder: 'you@example.com',
      displayName: '显示名（可选）',
      displayNamePlaceholder: '留空则使用用户名',
      password: '密码',
      passwordPlaceholder: '至少 8 位',
      confirmPassword: '确认密码',
      confirmPasswordPlaceholder: '再次输入密码',
      submit: '注册并登录',
    },
    validation: {
      passwordMismatch: '两次输入的密码不一致',
      usernameRequired: '请输入用户名',
      usernameLength: '用户名长度需在 3-32 之间',
      emailRequired: '请输入邮箱',
      emailInvalid: '请输入合法的邮箱地址',
      passwordRequired: '请输入密码',
      passwordMinLength: '密码至少 8 位',
      confirmPasswordRequired: '请再次输入密码',
    },
    messages: {
      success: '注册成功，欢迎使用 LinguaFlow',
      failed: '注册失败，请稍后重试',
    },
    links: {
      hasAccount: '已经有账号了？',
      login: '去登录',
    },
  },
  dashboard: {
    greeting: {
      named: '欢迎回来，{name}',
      anonymous: '欢迎使用 LinguaFlow',
    },
    intro: '这是 LinguaFlow 的工作台首页，查看您的翻译任务统计和最近活动。',
    stats: {
      apiCalls: 'API 调用',
      inputTokens: '输入 Token',
      outputTokens: '输出 Token',
      segmentCount: '任务段数',
    },
    quickActions: {
      createJob: {
        title: '新建任务',
        description: '创建一个新的翻译任务',
      },
      viewJobs: {
        title: '查看任务',
        description: '查看所有翻译任务',
      },
      manageOrganizations: {
        title: '管理组织',
        description: '管理您的组织设置',
      },
    },
    activity: {
      title: '最近活动',
      empty: '暂无活动记录',
      actions: {
        create: '创建了',
        update: '更新了',
        delete: '删除了',
        complete: '完成了',
        fail: '失败了',
        approve: '审核通过了',
        reject: '驳回了',
      },
      relativeTime: {
        justNow: '刚刚',
        minutesAgo: '{count} 分钟前',
        hoursAgo: '{count} 小时前',
        daysAgo: '{count} 天前',
      },
    },
    jobStatus: {
      title: '任务状态概览',
      total: '总计 {count} 个任务',
      successRate: '{percent}% 成功率',
      completed: '已完成',
      failed: '失败',
    },
  },
  about: {
    title: '关于 LinguaFlow',
    description:
      'LinguaFlow 是一个用于批量、高质量翻译的工作台。前端基于 Vue 3 + Vue Router 文件式路由 + Pinia + Naive UI 实现，样式由 Tailwind CSS 辅助布局。',
    sourcePrefix: '当前页面来自',
    sourceSuffix: '，自动映射为路径',
  },
  api: {
    errors: {
      requestNotSent: '请求未送达，请检查网络或服务器地址',
      serverReturned: '服务器返回 {status}',
      loginFailed: '登录失败',
      registerFailed: '注册失败',
      refreshSessionFailed: '刷新会话失败',
      fetchCurrentUserFailed: '获取当前用户失败',
      fetchStatsFailed: '获取用量统计失败',
      fetchActivityFailed: '获取活动日志失败',
      loadStatsFailed: '加载统计失败',
      loadActivityFailed: '加载活动失败',
    },
  },
} as const

export default messages
