# Leo-Go

基于 Go 重写的多平台 AI 图像/视频生成 API 网关，兼容 OpenAI API 协议。

## 功能

- **OpenAI 兼容 API** — `/v1/images/generations`、`/v1/chat/completions`、`/v1/video/generations`、`/v1/models`
- **Token 池管理** — 轮询/随机策略，自动失效、额度耗尽检测
- **管理后台** — 玻璃拟态风格 Web UI，支持 Token CRUD、配置管理、日志查看
- **Provider 架构** — 可扩展的平台插件系统，预留 Leonardo.ai、Stability AI 等接入
- **单二进制部署** — 零 CGO 依赖，编译产物为单个可执行文件

## 快速开始

### 本地运行

```bash
# 编译
go build -o leo-go.exe ./cmd/server/

# 运行（默认端口 8787）
./leo-go.exe

# 指定端口
./leo-go.exe -port 8800

# 指定配置文件
./leo-go.exe -config ./my-config.json
```

### Docker 运行

```bash
docker compose up -d
```

## 管理后台

启动后访问 `http://localhost:8787/`，默认账号密码 `admin / admin`。

功能包括：
- Token 添加（单个/批量/文件上传）
- Token 状态管理（启用/禁用/删除）
- 系统配置（代理、重试策略、轮询策略）
- 请求日志查看

## API 端点与调用方式

### 1. OpenAI 兼容 API

本系统完全兼容 OpenAI 的 `/v1` 协议，可以直接将其配置为 base_url 供其他客户端使用，或使用标准的 curl 进行请求。调用时请在 HTTP Header 中传递鉴权用的 `Authorization: Bearer <API_KEY>`。

#### 图像生成 (`/v1/images/generations`)
用于调用图像生成模型。
```bash
curl -X POST http://localhost:8787/v1/images/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "一只可爱的猫咪，赛博朋克风格",
    "model": "adobe-firefly", 
    "n": 1,
    "size": "1024x1024"
  }'
```

#### 视频生成 (`/v1/video/generations`)
用于调用视频生成模型。

> **💡 模型调用说明：`seedance-2.0` / `seedance-2.0-fast`**
> - **支持尺寸（比例）**：9:16、16:9、1:1（默认 720p，对应 `size` 参数如 `720x1280`、`1280x720`、`720x720`）
> - **支持时长**：4-15 秒（通过 Leonardo 专属高级 API 可通过 `duration` 参数控制时长）

```bash
curl -X POST http://localhost:8787/v1/video/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "猫咪在屋顶奔跑",
    "model": "seedance-2.0-fast",
    "n": 1,
    "size": "1280x720"
  }'
```

---

### 2. Leonardo 专属后台 API

对于需要深入控制 Leonardo 视频生成的场景，系统提供了一套高级接口（需要传入有效的 `token_id`），支持**首尾帧引导**和**多图参考引导**两种模式。

#### 2.1 上传参考图 (`/api/v1/leonardo/upload-image`)
上传用于首尾帧或参考引导的图像文件。支持 form-data 上传，需带入有效 `token_id`。

```bash
curl -X POST http://localhost:8787/api/v1/leonardo/upload-image \
  -F "file=@/path/to/image.jpg" \
  -F "token_id=你的Token_ID"
```
响应中将包含 `image_id`，用于后续生成。

#### 2.2 提交视频生成任务 (`/api/v1/leonardo/generate`)

##### 模式一：首尾帧引导（Start Frame / End Frame）
指定视频的首帧和尾帧图片，生成从图A过渡到图B的视频。对应 Leonardo 的 `guidances.start_frame` / `guidances.end_frame`。

```bash
curl -X POST http://localhost:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "你的Token_ID",
    "prompt": "从图一到图二，武侠视频",
    "model": "seedance-2.0-fast",
    "public": true,
    "duration": 4,
    "width": 720,
    "height": 1280,
    "start_frame": [
      {"id": "首帧image_id", "type": "UPLOADED"}
    ],
    "end_frame": [
      {"id": "尾帧image_id", "type": "UPLOADED"}
    ]
  }'
```

> **参数说明：**
> - `start_frame`: 视频的第一帧画面（数组，通常只放一张图）
> - `end_frame`: 视频的最后一帧画面（数组，通常只放一张图）
> - `public`: 是否在 Leonardo 社区公开展示（默认 `true`，传 `false` 则私密）
> - 可以只传 `start_frame` 或只传 `end_frame`，也可以同时传递

##### 模式二：多图参考引导（Image Reference）
提供参考图片影响生成风格/内容，但不锁定为具体帧。对应 Leonardo 的 `guidances.image_reference`。

```bash
curl -X POST http://localhost:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "你的Token_ID",
    "prompt": "风吹过麦田",
    "model": "seedance-2.0",
    "public": true,
    "duration": 5,
    "width": 1280,
    "height": 720,
    "image_guidance": [
      {"id": "参考图1_id", "type": "UPLOADED", "strength": "HIGH"},
      {"id": "参考图2_id", "type": "UPLOADED", "strength": "MID"}
    ]
  }'
```

> **参数说明：**
> - `image_guidance`: 多图参考数组，每张图可设置 `strength`（`LOW` / `MID` / `HIGH`）
> - `public`: 是否在 Leonardo 社区公开展示（默认 `true`）
> - 两种模式可以**混合使用**（同时传 `start_frame` + `image_guidance` 等）

##### 模式三：视频参考引导（Video Reference）
上传一段视频作为参考来生成新视频。对应 Leonardo 的 `guidances.video_reference_base`。

```bash
curl -X POST http://localhost:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "你的Token_ID",
    "prompt": "广告视频",
    "model": "seedance-2.0-fast",
    "public": false,
    "duration": 4,
    "width": 1280,
    "height": 720,
    "video_reference": [
      {"id": "上传的video_id", "type": "UPLOADED", "duration": 7.918}
    ]
  }'
```

> **参数说明：**
> - `video_reference`: 视频参考数组（通常只放一个视频）
> - `id`: 通过上传接口获取的视频文件 ID
> - `duration`: 原始视频时长（秒，浮点数）

##### 混合模式：图片 + 视频同时参考
所有引导模式可以**自由组合**。以下示例同时使用图片参考和视频参考：

```bash
curl -X POST http://localhost:8787/api/v1/leonardo/generate \
  -H "Content-Type: application/json" \
  -d '{
    "token_id": "你的Token_ID",
    "prompt": "广告视频",
    "model": "seedance-2.0-fast",
    "public": true,
    "duration": 4,
    "width": 720,
    "height": 1280,
    "image_guidance": [
      {"id": "图片_id", "type": "UPLOADED", "strength": "MID"}
    ],
    "video_reference": [
      {"id": "视频_id", "type": "UPLOADED", "duration": 7.918}
    ]
  }'
```

> **可组合的引导类型汇总：**
>
> | 字段 | GraphQL 路径 | 用途 |
> |------|-------------|------|
> | `start_frame` | `guidances.start_frame` | 锁定视频首帧 |
> | `end_frame` | `guidances.end_frame` | 锁定视频尾帧 |
> | `image_guidance` | `guidances.image_reference` | 图片风格/内容引导 |
> | `video_reference` | `guidances.video_reference_base` | 视频参考引导 |

#### 2.3 查询任务状态 (`/api/v1/leonardo/status?id=GENERATION_ID&token_id=TOKEN_ID`)
视频生成为异步任务，调用该接口拉取状态（PENDING / COMPLETE / FAILED），完成时会返回视频和预览图下载链接。

---

### 3. 系统管理 API

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/v1/auth/login` | POST | 管理员登录 |
| `/api/v1/tokens` | GET/POST | Token 列表/添加 |
| `/api/v1/tokens/batch` | POST | 批量添加 Token |
| `/api/v1/tokens/{id}` | DELETE | 删除 Token |
| `/api/v1/tokens/{id}/status` | PUT | 修改 Token 状态 |
| `/api/v1/leonardo/credits` | GET | 查询可用代币余额 |
| `/api/v1/config` | GET/PUT | 系统配置 |

## 配置

编辑 `config/config.json`：

```json
{
  "admin_username": "admin",
  "admin_password": "admin",
  "api_key": "",
  "proxy": "",
  "generate_timeout": 300,
  "retry_enabled": true,
  "retry_max_attempts": 3,
  "token_rotation_strategy": "round_robin"
}
```

## 项目结构

```
Leo-Go/
├── cmd/server/main.go          # 入口
├── internal/
│   ├── config/config.go        # 配置管理
│   ├── handler/
│   │   ├── generation.go       # 生成 API 处理器
│   │   └── admin.go            # 管理 API 处理器
│   ├── provider/
│   │   ├── provider.go         # Provider 接口
│   │   ├── registry.go         # Provider 注册中心
│   │   └── adobe/
│   │       ├── client.go       # Adobe Firefly 客户端
│   │       └── models.go       # 模型目录 & Payload 构建
│   ├── store/
│   │   ├── sqlite.go           # SQLite 持久化
│   │   └── job.go              # 异步 Job 存储
│   └── token/
│       └── manager.go          # Token 池管理器
├── static/                     # 前端文件
├── config/config.json          # 默认配置
├── Dockerfile
└── docker-compose.yml
```

## 扩展新平台

实现 `provider.Provider` 接口即可接入新平台：

```go
type MyProvider struct{}

func (p *MyProvider) Name() string { return "my_platform" }
func (p *MyProvider) Generate(ctx context.Context, req ImageRequest) (*JobResult, error) { ... }
func (p *MyProvider) SupportedModels() []ModelInfo { ... }
// ... 实现其他接口方法

// 在 main.go 中注册：
registry.Register(&MyProvider{})
```
