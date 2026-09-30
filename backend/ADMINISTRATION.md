# 管理员初始化与维护

维护命令直接使用部署配置和数据库权限，在 HTTP 服务尚未就绪时也能运行。`linguaflow init` 只生成配置模板；数据库首次初始化由 `serve/local` 启动流程或 `admin initialize` 完成。

```text
linguaflow admin initialize --config ./server.yaml
linguaflow admin create --config ./server.yaml --username another-admin --email another@example.com --password-file ./admin-password
linguaflow admin recover --config ./server.yaml --username admin --password-stdin
```

首次 serve 初始化需要 `bootstrap.admin`，也可向 `admin initialize` 提供 `--username`、`--email` 和互斥的 `--password-file` / `--password-stdin`。密码输入保留空格，只移除一个末尾换行，不接受明文密码命令行参数。

初始化将管理员、注册政策和完成标记原子提交。已初始化实例再次执行初始化只验证状态，不覆盖政策、角色或密码。`admin create/recover` 仅适用于已初始化 serve 实例；recover 要求账户已经存在，将其恢复为活跃管理员、重置密码并撤销其 refresh tokens。已签发的 access token 仍遵守原到期时间。所有维护操作都不重新运行 bootstrap。
