# 常见问题

## 安装与启动

### Docker 启动后无法访问？

检查端口映射是否正确：

```bash
docker ps  # 确认容器正在运行
docker logs linguaflow  # 查看日志
```

确保端口未被占用，并检查防火墙设置。

### 从源码构建失败？

1. 确认 Go 版本 >= 1.21
2. 确认 Node.js 版本 >= 20
3. 确认已安装 pnpm
4. 运行 `task backend:install` 和 `task frontend:install` 安装依赖

## AI 后端配置

### API Key 无效？

- 确认 API Key 是否正确复制（无多余空格）
- 确认 API Key 是否有效（未过期、未被禁用）
- 检查 Base URL 是否正确（如果使用了自定义代理）

### 翻译速度很慢？

- 检查网络连接到 AI 服务的速度
- 尝试使用不同的 AI 模型（较小的模型通常更快）
- 减少上下文窗口大小
- 检查是否有速率限制

### 翻译质量不理想？

- 使用术语表确保术语一致性
- 自定义提示词模板
- 提供更多上下文信息
- 尝试不同的 AI 模型
- 使用多轮翻译流水线

## 文件格式

### EPUB 翻译后排版混乱？

- 确认 EPUB 文件格式是否标准
- 尝试按章节分批翻译
- 检查是否有特殊格式需要保护

### 字幕文件时间码被修改？

LinguaFlow 会自动保护时间码，如果发现时间码被修改：

- 检查文件编码是否为 UTF-8
- 确认文件格式是否正确

## 数据与存储

### 数据存储在哪里？

- **本地模式** — SQLite 数据库文件，默认在 `./data/linguaflow.db`
- **服务器模式** — 同上，可通过 `--db` 参数指定路径

### 如何备份数据？

复制 SQLite 数据库文件即可：

```bash
cp ./data/linguaflow.db ./backup/linguaflow_backup.db
```

### 如何迁移数据？

将数据库文件复制到新环境即可。

## 性能与限制

### 支持多大的文件？

文件大小限制取决于：

- AI 服务的上下文窗口大小
- 可用内存
- 网络带宽

建议大文件按章节或段落分批翻译。

### 支持多少并发翻译？

并发数取决于：

- AI 服务的速率限制
- 系统资源
- 配置的并发数设置

## 更多问题

如果以上内容没有解决您的问题：

- 查看 [GitHub Issues](https://github.com/MeowSalty/LinguaFlow/issues) 寻找类似问题
- [提交新 Issue](https://github.com/MeowSalty/LinguaFlow/issues/new) 描述您的问题
