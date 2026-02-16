# 🔥 FireCloud 教学云盘

专为 Windows 教室环境设计的单文件教学云盘系统。

## 项目结构

```
FireCloud/
├── main.go              # Go 后端（全部逻辑）
├── go.mod               # Go 模块定义
├── build.bat            # 编译脚本
├── static/
│   └── index.html       # 前端界面（通过 go:embed 打包进 EXE）
└── README.md
```

## 核心功能

| 功能 | 说明 |
|------|------|
| 📁 文件管理 | 浏览、上传、删除、新建文件夹 |
| 📂 文件夹拖拽上传 | 使用 `webkitGetAsEntry` 递归解析目录结构 |
| 🌐 H5 课件托管 | 文件夹内含 `index.html` 时自动作为静态网站运行 |
| 🎬 视频播放器 | YouTube 风格，右侧自动加载播放列表 |
| 🖼️ 图片灯箱 | 全屏预览 + 方向键切换 |
| 🔒 BasicAuth | 内置账号密码认证 |
| 📡 HTTP Range | 支持大文件视频拖动进度条 |
| 💾 流式 IO | 大文件上传不占内存 |

## 安装 Go

1. 下载：https://go.dev/dl/
2. 安装后重启终端，验证：`go version`

## 编译

```bat
cd D:\程序设计\GO\EDU\FireCloud
build.bat
```

或手动执行：

```bat
SET CGO_ENABLED=0
SET GOOS=windows  
SET GOARCH=amd64
go build -ldflags "-s -w -H windowsgui" -o FireCloud.exe main.go
```

## 运行

双击 `FireCloud.exe`，浏览器自动打开 http://localhost:8080

- **账号**: `admin`
- **密码**: `fire2026`
- **管理目录**: `D:\Fire`（自动创建）

## index.html 优先规则

| 场景 | 行为 |
|------|------|
| `D:\Fire\课件\index.html` 存在 | 访问 `/课件/` 直接展示 H5 课件 |
| `D:\Fire\资料\` 无 index.html | 显示文件管理界面 |
| URL 带 `?manage=1` | 强制显示文件管理界面 |

## 修改配置

在 `main.go` 顶部常量区修改：

```go
const (
    listenAddr = ":8080"      // 端口
    rootDir    = `D:\Fire`    // 管理目录
    authUser   = "admin"      // 账号
    authPass   = "fire2026"   // 密码
)
```
