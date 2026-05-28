# Leo-Go

基于 Go 的视频生成网关，提供：

- OpenAI 兼容接口
  - `GET /v1/models`
  - `POST /v1/video/generations`
  - `GET /v1/video/generations/{generation_id}`
- Leonardo 管理接口
  - Token 导入、刷新、积分查询
  - 视频生成、高级引导参数、任务状态查询
- 管理后台
  - Token 管理
  - 代理与重试配置
  - 请求日志

## 当前模型

`/v1/models` 当前返回：

- `video-2.0`
- `video-2.0-fast`
- `sora-2`
- `ko3`

兼容原模型名：`seedance-2.0` 会映射到 `video-2.0`，`seedance-2.0-fast` 会映射到 `video-2.0-fast`。

模型名映射规则：

| 下游可传模型名 | 实际上游模型名 | 说明 |
| --- | --- | --- |
| `video-2.0` | `seedance-2.0` | 推荐使用的新标准模型名 |
| `video-2.0-fast` | `seedance-2.0-fast` | 推荐使用的新快速模型名 |
| `sora-2` | `sora2` | Sora 2 视频上游模型 |
| `ko3` | `kling-video-o-3` | Kling O3 视频上游模型 |
| `kling-o3` | `kling-video-o-3` | 兼容旧调用格式 |
| `seedance-2.0` | `seedance-2.0` | 兼容旧调用格式 |
| `seedance-2.0-fast` | `seedance-2.0-fast` | 兼容旧调用格式 |
| `kling-video-o-3` | `kling-video-o-3` | 兼容上游模型名 |

下游请求建议优先使用 `video-2.0` 和 `video-2.0-fast`。服务内部会在调用 Leonardo 上游前自动转换为对应的 `seedance-*` 模型名，Token 成功次数统计和组合耗尽自动禁用也会按映射后的模型正确计入 `S` 或 `F`。

`sora-2` 会按 Leonardo Web 端上游格式映射为 `sora2`，默认 `duration=8`、`size=720x1280`，支持文生视频和 start-frame 图生视频参数。`sora-2` 仅支持 `720x1280`（9:16）和 `1280x720`（16:9），时长仅支持 `4`、`8`、`12` 秒，最多上传一张图片。
`ko3` 会映射为 Leonardo 上游 `kling-video-o-3`，默认 `duration=3`、`size=1080x1920`、`mode=RESOLUTION_1080`、`motion_has_audio=true`，支持文生视频、`image_reference` 图生视频、首尾帧模式和参考视频生视频。显式配置支持 `1440x1440`、`1080x1920`、`1920x1080`，时长支持 `3-15` 秒；参考视频模式未传尺寸时默认 `size=0x0`。

`video-2.0` 和 `video-2.0-fast` 当前统一按下面的口径调用：

- 支持尺寸（比例）：`9:16`、`16:9`、`1:1`
- 默认分辨率：`720p`
- 对应 `size` 示例：`720x1280`、`1280x720`、`960x960`
- 支持时长：`4-15` 秒

## 快速开始

### 本地运行

```bash
go build -o leo-go.exe ./cmd/server/
./leo-go.exe
```

默认监听：`http://127.0.0.1:8787`

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

## 自动化 Cookie 导入接口

用于把自动化程序获取到的 Leonardo Cookie 导入到 Token 池。自动化程序只需要知道服务地址和 Token 池导入密钥，不需要管理员账号密码。

导入密钥在后台 `系统配置 -> 账号与安全 -> Token 池导入密钥` 中设置，对应配置项为 `cookie_import_api_key`。

接口：

```text
POST /api/v1/tokens/import-cookie
```

鉴权支持两种写法，任选一种：

```http
Authorization: Bearer YOUR_COOKIE_IMPORT_API_KEY
```

```http
X-Import-Key: YOUR_COOKIE_IMPORT_API_KEY
```

### 单个导入

```bash
curl -X POST http://127.0.0.1:8787/api/v1/tokens/import-cookie \
  -H "Authorization: Bearer YOUR_COOKIE_IMPORT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "account@example.com",
    "cookie": "__Secure-better-auth.session_token=...; ..."
  }'
```

### 批量导入

```bash
curl -X POST http://127.0.0.1:8787/api/v1/tokens/import-cookie \
  -H "Authorization: Bearer YOUR_COOKIE_IMPORT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "items": [
      {
        "name": "account-a@example.com",
        "cookie": "__Secure-better-auth.session_token=...; ..."
      },
      {
        "name": "account-b@example.com",
        "cookie": "__Secure-better-auth.session_token=...; ..."
      }
    ]
  }'
```

也支持只传 Cookie 字符串数组：

```json
{
  "cookies": [
    "__Secure-better-auth.session_token=...; ...",
    "__Secure-better-auth.session_token=...; ..."
  ]
}
```

批量导入不限制条数。导入成功后会校验 Cookie、更新账号邮箱、积分、过期时间，并默认开启 `auto_refresh`。如果识别到同一个 Leonardo 账号，会覆盖旧 Cookie，避免 Token 池产生同账号重复记录。

响应示例：

```json
{
  "ok": true,
  "total": 1,
  "success_count": 1,
  "failed_count": 0,
  "duplicate_count": 0,
  "overwritten_count": 0,
  "items": [
    {
      "index": 0,
      "status": "active",
      "detail": "imported",
      "token_id": "5UNuopWd",
      "token_account_email": "account@example.com",
      "credits": 8500,
      "overwritten": false,
      "duplicate": false
    }
  ]
}
```

接口响应不会回显原始 Cookie。

## OpenAI 兼容调用

### 1. 查询模型

```bash
curl http://127.0.0.1:8787/v1/models \
  -H "Authorization: Bearer YOUR_API_KEY"
```

### 2. 生成视频

统一入口：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "A cinematic drone shot over a neon city at dusk",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720"
  }'
```

Async change:

- `POST /v1/video/generations` now submits the job only.
- The submit endpoint returns `202 Accepted` with `generation_id`.
- Clients must poll `GET /v1/video/generations/{generation_id}` until the job reaches `succeeded` or `failed`.

Submit response example:

```json
{
  "id": "1f149999-aaaa-bbbb-cccc-1234567890ab",
  "object": "video.generation",
  "created": 1770000000,
  "model": "video-2.0-fast",
  "status": "in_progress",
  "poll_url": "/v1/video/generations/1f149999-aaaa-bbbb-cccc-1234567890ab",
  "request_id": "1f149999-aaaa-bbbb-cccc-1234567890ab"
}
```

Poll example:

```bash
curl http://127.0.0.1:8787/v1/video/generations/1f149999-aaaa-bbbb-cccc-1234567890ab \
  -H "Authorization: Bearer YOUR_API_KEY"
```

Successful poll response example:

```json
{
  "id": "1f149999-aaaa-bbbb-cccc-1234567890ab",
  "object": "video.generation",
  "created": 1770000000,
  "model": "video-2.0-fast",
  "status": "succeeded",
  "request_id": "1f149999-aaaa-bbbb-cccc-1234567890ab",
  "data": [
    {
      "url": "https://example.com/final.mp4"
    }
  ]
}
```

Failed poll response example:

```json
{
  "id": "1f149999-aaaa-bbbb-cccc-1234567890ab",
  "object": "video.generation",
  "created": 1770000000,
  "model": "video-2.0-fast",
  "status": "failed",
  "request_id": "1f149999-aaaa-bbbb-cccc-1234567890ab",
  "error": {
    "message": "Generation failed in Leonardo",
    "type": "server_error"
  }
}
```

常用参数：

- `prompt`: 提示词
- `model`: 模型名
- `duration`: 视频时长（秒）
- `size`: 输出尺寸，例如 `1280x720`、`720x1280`

### ko3 下游调用标准

下游调用 `ko3` 时统一使用 `POST /v1/video/generations`，`model` 固定传 `ko3`。`kling-o3` 和 `kling-video-o-3` 仅作为兼容别名保留，不建议新接入继续使用。

通用规则：

- 文生视频、图片生视频、首尾帧、多图生视频：可以传 `duration` 和 `size`
- `duration` 支持 `3-15` 秒
- `size` 支持 `1440x1440`、`1080x1920`、`1920x1080`
- 文生视频、单图生视频、多图生视频、首尾帧模式要求 token 剩余积分不少于 `4200`
- 带视频参考的模式：下游默认不传 `size` 和 `duration`
- 带视频参考时，服务会按上游 Web 抓包默认发送 `width=0`、`height=0`、`duration=5`
- 带视频参考的模式要求 token 剩余积分不少于 `3400`
- 所有请求都需要传 `prompt`
- 远程图片优先使用 `image_url` / `image_urls` / `start_image_url` / `end_image_url`
- 远程视频优先使用 `video_url`

推荐字段：

| 模式 | 必填字段 | 可选字段 | 说明 |
| --- | --- | --- | --- |
| 文生视频 | `prompt`, `model` | `duration`, `size` | 不传时默认 `duration=3`、`size=1080x1920` |
| 单图生视频 | `prompt`, `model`, `image_url` | `duration`, `size` | 服务会上传图片并转成 `guidances.image_reference` |
| 多图生视频 | `prompt`, `model`, `image_urls` | `duration`, `size` | `image_urls` 按数组顺序作为多图参考 |
| 首尾帧 | `prompt`, `model`, `start_image_url`, `end_image_url` | `duration`, `size` | 服务会分别转成 `start_frame` 和 `end_frame` |
| 视频生视频 | `prompt`, `model`, `video_url` | 不建议传 `duration`, `size` | 默认使用上游视频参考参数 |
| 图片 + 视频生视频 | `prompt`, `model`, `image_url`, `video_url` | 不建议传 `duration`, `size` | 图片作为参考图，视频作为参考视频 |
| 多图 + 视频生视频 | `prompt`, `model`, `image_urls`, `video_url` | 不建议传 `duration`, `size` | 多张图片作为参考图，视频作为参考视频 |

积分门槛：

| 模式 | 最低剩余积分 |
| --- | --- |
| 文生视频 | `4200` |
| 单图生视频 | `4200` |
| 多图生视频 | `4200` |
| 首尾帧 | `4200` |
| 视频生视频 | `3400` |
| 图片 + 视频生视频 | `3400` |
| 多图 + 视频生视频 | `3400` |

标准 JSON 示例：

```json
{
  "prompt": "龟兔赛跑",
  "model": "ko3",
  "duration": 3,
  "size": "1080x1920"
}
```

```json
{
  "prompt": "猫咪跳舞",
  "model": "ko3",
  "duration": 3,
  "size": "1080x1920",
  "image_url": "https://example.com/cat.png"
}
```

```json
{
  "prompt": "动物世界",
  "model": "ko3",
  "duration": 3,
  "size": "1080x1920",
  "image_urls": [
    "https://example.com/a.png",
    "https://example.com/b.png",
    "https://example.com/c.png"
  ]
}
```

```json
{
  "prompt": "从图一过渡到图二",
  "model": "ko3",
  "duration": 3,
  "size": "1080x1920",
  "start_image_url": "https://example.com/start.png",
  "end_image_url": "https://example.com/end.png"
}
```

```json
{
  "prompt": "把视频中的香水替换成牙膏",
  "model": "ko3",
  "video_url": "https://example.com/source.mp4"
}
```

```json
{
  "prompt": "把视频中的香水替换成图片里的小熊",
  "model": "ko3",
  "image_url": "https://example.com/bear.png",
  "video_url": "https://example.com/source.mp4"
}
```

```json
{
  "prompt": "用多张图片替换视频主体",
  "model": "ko3",
  "image_urls": [
    "https://example.com/a.png",
    "https://example.com/b.png"
  ],
  "video_url": "https://example.com/source.mp4"
}
```

### 2.1 文生视频

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "A cinematic drone shot over a neon city at dusk",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720"
  }'
```

`sora-2` 文生视频示例：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "龟兔赛跑",
    "model": "sora-2",
    "duration": 8,
    "size": "720x1280"
  }'
```

`ko3` 文生视频示例：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "龟兔赛跑",
    "model": "ko3",
    "duration": 3,
    "size": "1080x1920"
  }'
```

### 2.2 单图生成视频（图生视频）

最简单的写法是传 `image_url`。服务会自动把远程图片上传到 Leonardo，再作为首帧参考：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Animate this portrait with subtle camera motion",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "720x1280",
    "image_url": "https://example.com/portrait.jpg"
  }'
```

`sora-2` 图生视频最多支持一张起始图，可使用 `image_url`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "武侠视频",
    "model": "sora-2",
    "duration": 8,
    "size": "720x1280",
    "image_url": "https://example.com/start.png"
  }'
```

`ko3` 图生视频同样可以使用 `image_url`，服务会把它转换为 Leonardo 上游的 `guidances.image_reference`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "猫咪跳舞",
    "model": "ko3",
    "duration": 3,
    "size": "1080x1920",
    "image_url": "https://example.com/cat.png"
  }'
```

`ko3` 多图生视频可使用 `image_guidance`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "动物世界",
    "model": "ko3",
    "duration": 3,
    "size": "1080x1920",
    "image_guidance": [
      {"id": "f02f2740-708a-4333-9253-f2bf788fe201"},
      {"id": "b3941f10-34ab-4535-8725-ff44a3f2ca21"},
      {"id": "09eff9d4-284a-4454-aa42-2a5c64906af6"},
      {"id": "b9b7f87c-3312-44c6-a92d-a81745ec0635"}
    ]
  }'
```

`ko3` 首尾帧模式可使用 `start_frame` 和 `end_frame`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "从图一过渡到图二",
    "model": "ko3",
    "duration": 3,
    "size": "1080x1920",
    "start_frame": [
      {"id": "f02f2740-708a-4333-9253-f2bf788fe201"}
    ],
    "end_frame": [
      {"id": "09eff9d4-284a-4454-aa42-2a5c64906af6"}
    ]
  }'
```

`ko3` 参考视频生视频可使用 `video_reference`。带视频参考时不需要传 `size` 和 `duration`；服务会默认按 Leonardo Web 端抓包发送 `width=0`、`height=0`、`duration=5`。如需覆盖，也可以显式传 `size` 或 `width` / `height`，并用 `duration` 控制生成时长：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "把视频中的香水替换成牙膏",
    "model": "ko3",
    "video_reference": [
      {
        "id": "fbeda0e3-a8b3-45d6-a22e-4e53da4148f9",
        "duration": 7.918005
      }
    ]
  }'
```

`ko3` 图片 + 视频参考可同时传 `image_guidance` 和 `video_reference`，同样不需要传 `size` 和 `duration`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "把视频中的香水替换图片的小熊",
    "model": "ko3",
    "image_guidance": [
      {"id": "b9b7f87c-3312-44c6-a92d-a81745ec0635"}
    ],
    "video_reference": [
      {
        "id": "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc",
        "duration": 4.017007
      }
    ]
  }'
```

`ko3` 多图 + 视频参考也使用同一组字段，只需要在 `image_guidance` 中传多张图：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "用多张图片替换视频主体",
    "model": "ko3",
    "image_guidance": [
      {"id": "b9b7f87c-3312-44c6-a92d-a81745ec0635"},
      {"id": "09eff9d4-284a-4454-aa42-2a5c64906af6"},
      {"id": "f02f2740-708a-4333-9253-f2bf788fe201"}
    ],
    "video_reference": [
      {
        "id": "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc",
        "duration": 4.017007
      }
    ]
  }'
```

`sora-2` 也可以显式使用 `start_frame`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "武侠视频",
    "model": "sora-2",
    "duration": 12,
    "size": "1280x720",
    "start_frame": [
      {
        "url": "https://example.com/start.png"
      }
    ]
  }'
```

`sora-2` 仅支持 `720x1280`（9:16）和 `1280x720`（16:9），时长仅支持 `4`、`8`、`12` 秒。

如果需要显式控制首帧和尾帧，也可以写成：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Animate this still into a short teaser",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720",
    "start_frame": [
      {
        "url": "https://example.com/start.png"
      }
    ],
    "end_frame": [
      {
        "url": "https://example.com/end.png"
      }
    ]
  }'
```

### 2.3 多图生成视频

推荐使用 `image_guidance`。每一项都可以直接传远程 `url`，服务会逐张上传并自动替换成 Leonardo 可用的参考图 ID。

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "图一的人物和图二的人物，在图三的场景里",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "720x1280",
    "image_guidance": [
      {
        "url": "https://example.com/character-1.png",
        "strength": "MID"
      },
      {
        "url": "https://example.com/character-2.png",
        "strength": "MID"
      },
      {
        "url": "https://example.com/scene.png",
        "strength": "MID"
      }
    ]
  }'
```

也支持更轻量的 `image_urls` 写法：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Use multiple references to create a short fashion ad",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720",
    "image_urls": [
      "https://example.com/ref-1.jpg",
      "https://example.com/ref-2.jpg",
      "https://example.com/ref-3.jpg"
    ]
  }'
```

### 2.4 视频生成视频（视频参考生成视频）

最简单的写法是传 `video_url`。服务会自动下载远程 mp4、上传到 Leonardo，然后作为视频参考：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "广告视频",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720",
    "video_url": "https://example.com/source.mp4"
  }'
```

当使用 `video_url` 或 `video_reference[].url` 时，服务会在上传远程视频后尽量自动解析源视频时长，并一并传给 Leonardo 的 `video_reference_base[].video.duration`，以更贴近 Leonardo Web 端的实际请求格式。

如果你已经有 Leonardo 侧的视频参考 ID，也可以显式写 `video_reference`：

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "广告视频",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720",
    "video_reference": [
      {
        "id": "LEONARDO_VIDEO_ID",
        "type": "UPLOADED",
        "duration": 4
      }
    ]
  }'
```

这是显式调用格式。这里的：

- 顶层 `duration` 表示生成结果的视频时长
- `video_reference[].duration` 表示参考视频本身的时长
- 当使用 `video_url` 或 `video_reference[].url` 时，服务会在上传远程视频后尽量自动解析源视频时长，并一并传给 Leonardo 的 `video_reference_base[].video.duration`
- 如果你直接传的是 Leonardo 侧已有的 `video_reference[].id`，推荐同时显式补上 `video_reference[].duration`

视频参考素材限制：
- 上游要求视频参考素材的分辨率必须在 720px 到 2160px 之间，否则会返回类似 `The video resolution is not supported. Please ensure the video is between 720px and 2160px.` 的错误。
- 这里的限制是参考视频本身的宽高，不是生成结果的 `size`。例如 416x752 这类视频会被上游拒绝；请先转码、缩放或补边到 720x1280、1280x720、1080x1920 等符合范围的尺寸后，再作为 `video_url` 或 `video_reference[].url` 使用。


Implementation note:
- For `video_url` and `video_reference[].url`, the service downloads the remote mp4, calls Leonardo `UploadImage(uploadType=INIT, extension=mp4)`, uploads to S3, polls `uploaded_media`, waits for `status = COMPLETE`, and then reuses that upload as the video reference for `Generate`.
- The top-level `duration` is the generated result duration, while `video_reference[].duration` is the source reference video duration.
- For `video_url` and `video_reference[].url`, the service also waits for `uploaded_media.status = COMPLETE` before starting generation.

Validated sample:
```json
{
  "prompt": "广告视频",
  "model": "video-2.0-fast",
  "duration": 4,
  "size": "720x1280",
  "video_url": "https://img688.com/file/1777636472339_0430.mp4"
}
```

多视频参考：

当你需要同时传多个视频参考时，请使用 `video_reference` 对象数组。
不要传纯 `string[]`。

请求示例：

```json
{
  "prompt": "广告视频",
  "model": "video-2.0-fast",
  "duration": 8,
  "size": "864x496",
  "video_reference": [
    {
      "url": "https://example.com/video-1.mp4"
    },
    {
      "url": "https://example.com/video-2.mp4"
    }
  ]
}
```

如果你已经有 Leonardo 侧已上传的视频 ID：

```json
{
  "prompt": "广告视频",
  "model": "video-2.0-fast",
  "duration": 8,
  "size": "864x496",
  "video_reference": [
    {
      "id": "LEONARDO_VIDEO_ID_1",
      "duration": 4.041667
    },
    {
      "id": "LEONARDO_VIDEO_ID_2",
      "duration": 3.833333
    }
  ]
}
```

已验证成功的多视频样例：

```json
{
  "prompt": "广告视频",
  "model": "video-2.0-fast",
  "duration": 8,
  "size": "864x496",
  "video_reference": [
    {
      "url": "https://img688.com/file/1777622416553_327f3b9260c941aaa24fbc691e8aa0db.mp4"
    },
    {
      "url": "https://img688.com/file/1777626569686_11d4b80b6ddb4c3f40793f68f0ae516a.mp4"
    }
  ]
}
```

### 2.5 图片 + 视频混合参考生成视频

如果要同时传图片参考和视频参考，推荐使用 `image_guidance + video_reference` 这一组字段。
这是 Leonardo 上游实际使用的混合参考格式：图片会作为 `image_reference`，视频会作为 `video_reference_base`。

```bash
curl -X POST http://127.0.0.1:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "广告视频",
    "model": "video-2.0-fast",
    "duration": 4,
    "size": "1280x720",
    "image_guidance": [
      {
        "url": "https://example.com/reference-image.png",
        "strength": "MID"
      }
    ],
    "video_reference": [
      {
        "url": "https://example.com/reference-video.mp4"
      }
    ]
  }'
```

为了兼容简单调用，接口现在也支持直接传：

```json
{
  "image_url": "https://example.com/reference-image.png",
  "video_url": "https://example.com/reference-video.mp4"
}
```

当请求里存在视频参考时，`image_url` 会自动按图片参考处理，而不是按单图首帧处理。

### 2.6 支持的扩展字段

- `image_url`
- `start_image_url`
- `end_image_url`
- `image_urls`
- `image_guidance`
- `start_frame`
- `end_frame`
- `video_url`
- `video_reference`

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
    "model": "video-2.0-fast",
    "public": false,
    "duration": 4,
    "width": 720,
    "height": 1280
  }'
```

高级接口里的图片/视频引导字段，既支持直接传 Leonardo `id`，也支持传远程 `url`。

图片示例：

```bash
curl -X POST http://127.0.0.1:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "YOUR_TOKEN_ID",
    "prompt": "Animate the uploaded poster into a vertical teaser video",
    "model": "video-2.0-fast",
    "duration": 4,
    "width": 720,
    "height": 1280,
    "start_frame": [
      {
        "url": "https://example.com/poster.png"
      }
    ]
  }'
```

视频参考示例：

```bash
curl -X POST http://127.0.0.1:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "YOUR_TOKEN_ID",
    "prompt": "广告视频",
    "model": "video-2.0-fast",
    "duration": 4,
    "width": 720,
    "height": 1280,
    "video_reference": [
      {
        "url": "https://example.com/source.mp4",
        "duration": 4
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

最小示例：

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
├── cmd/server/main.go
├── internal/
│   ├── config/
│   ├── handler/
│   ├── provider/leonardo/
│   ├── reqlog/
│   ├── store/
│   └── token/
├── static/
├── config/config.json
├── Dockerfile
└── docker-compose.yml
```
