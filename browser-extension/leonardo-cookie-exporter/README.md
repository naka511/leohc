# Leonardo Cookie Exporter

这是一个给 Chrome / Edge 用的小扩展，用来快速导出 Leonardo 标准 Cookie JSON，便于直接导入 Leo-Go 后台。

## 功能

- 一键导出标准 JSON 文件
- 一键复制 Leonardo Cookie 字符串
- 默认兼容 Leo-Go 的“导入 Cookie”格式

导出的 JSON 结构如下：

```json
{
  "cookie": "__Secure-better-auth.session_token=...; __Secure-better-auth.session_data.0=...; CF_Access_Token=..."
}
```

## 安装方式

### Chrome / Edge 加载临时扩展

1. 打开浏览器扩展管理页
2. 开启“开发者模式”
3. 点击“加载已解压的扩展程序”
4. 选择当前目录：

```text
browser-extension/leonardo-cookie-exporter
```

## 使用方式

1. 先在浏览器里登录 `https://app.leonardo.ai`
2. 打开扩展
3. 点击“导出标准 JSON”
4. 把导出的 `.json` 文件拿到 Leo-Go 后台“导入 Cookie”使用

## 说明

- 默认勾选“包含全部 Leonardo Cookie”，兼容性更高
- 如果你只想拿最核心的登录字段，也可以取消勾选，只导出认证相关 Cookie
- 当前导出的是最简格式，只保留 `cookie` 字段，便于手动查看和导入
