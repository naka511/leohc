# Leo-Go

基于 Go 的 Seedance 视频生成 API 网关，提供 OpenAI 兼容接口和 Leonardo 管理后台。

## 当前能力

- OpenAI 兼容接口
  - `GET /v1/models`
  - `POST /v1/video/generations`
- Leonardo 管理接口
  - Token 导入、刷新、积分查询
  - Leonardo 视频生成、高级引导参数、任务状态查询
- 管理后台
  - Token 管理
  - 代理与重试配置
  - 请求日志

## 当前模型

`/v1/models` 当前只返回以下模型：

- `seedance-2.0`
- `seedance-2.0-fast`

## 快速开始

### 本地运行

```bash
go build -o leo-go.exe ./cmd/server/
./leo-go.exe
```

默认监听 `http://127.0.0.1:8787`

### Docker 运行

```bash
docker compose up -d
```

建议持久化以下目录：

- `/app/config`
- `/app/generated`

## 管理后台

启动后访问：

- `http://127.0.0.1:8787/`

默认账号密码取决于 `config/config.json`，如果没有配置则回退到：

- 用户名：`admin`
- 密码：`admin`

## OpenAI 兼容调用

### 1. 查询模型

```bash
curl http://127.0.0.1:8787/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY"
```

### 2. 生成视频

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "A cinematic drone shot over a neon city at dusk",
    "model": "seedance-2.0-fast",
    "duration": 5,
    "size": "1280x720"
  }'
```

支持模型：

- `seedance-2.0`
- `seedance-2.0-fast`

常用参数：

- `prompt`
- `model`
- `duration`
- `size`

单图 URL 图生视频也支持，默认会把 `image_url` 作为首帧参考：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Animate this portrait with subtle camera motion",
    "model": "seedance-2.0-fast",
    "duration": 5,
    "size": "720x1280",
    "image_url": "https://example.com/portrait.jpg"
  }'
```

也支持这些扩展字段：

- `image_url`
- `start_image_url`
- `end_image_url`
- `image_urls`
- `image_guidance`
- `start_frame`
- `end_frame`

## Leonardo 高级接口

### 上传参考图

```bash
curl -X POST http://127.0.0.1:8787/api/v1/leonardo/upload-image \
  -F "file=@/path/to/image.jpg" \
  -F "token_id=YOUR_TOKEN_ID"
```

### 提交生成任务

```bash
curl -X POST http://127.0.0.1:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "YOUR_TOKEN_ID",
    "prompt": "A fashion ad style vertical video",
    "model": "seedance-2.0-fast",
    "public": false,
    "duration": 5,
    "width": 720,
    "height": 1280
  }'
```

高级接口里的图片引导字段，既支持直接传 Leonardo `id`，也支持传远程图片 `url`。例如：

```bash
curl -X POST http://127.0.0.1:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "YOUR_TOKEN_ID",
    "prompt": "Animate the uploaded poster into a vertical teaser video",
    "model": "seedance-2.0-fast",
    "duration": 5,
    "width": 720,
    "height": 1280,
    "start_frame": [
      {
        "url": "https://example.com/poster.png"
      }
    ]
  }'
```

### 查询任务状态

```bash
curl "http://127.0.0.1:8787/api/v1/leonardo/status?id=GENERATION_ID&token_id=YOUR_TOKEN_ID"
```

## 配置

配置文件默认路径：

- `config/config.json`

一个最小示例：

```json
{
  "admin_username": "admin",
  "admin_password": "admin",
  "api_key": "",
  "proxy": "",
  "use_proxy": false,
  "generate_timeout": 300,
  "retry_enabled": true,
  "retry_max_attempts": 3,
  "token_rotation_strategy": "round_robin"
}
```

## 目录结构

```text
Leo-Go/
├─ cmd/server/main.go
├─ internal/
│  ├─ config/
│  ├─ handler/
│  ├─ provider/leonardo/
│  ├─ reqlog/
│  ├─ store/
│  └─ token/
├─ static/
├─ config/config.json
├─ Dockerfile
└─ docker-compose.yml
```
