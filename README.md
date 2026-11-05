# SwitchHosts-mygo

SwitchHosts mygo 框架重写版，感谢 oldj 的项目以及混元 4 免费算力贡献。

[![Windows CI/CD](https://github.com/SatanKiss/SwitchHost-mygo/actions/workflows/windows.yml/badge.svg)](https://github.com/SatanKiss/SwitchHost-mygo/actions/workflows/windows.yml)

## 本地构建

项目使用纯 Go 的 MyGo 原生 UI：无 cgo、无 WebView、无 Node 工具链。打包 CLI 通过 `go.mod` 的 `tool` 指令与运行库锁定同一版本，无需额外安装：

```bash
go tool mygo build -platform windows/amd64,windows/arm64
```

产物在 `build/windows-<arch>/`：带图标、清单和版本信息的 `SwitchHosts Native.exe`，以及 NSIS 安装包 `switchhosts-native-setup-<version>-<arch>.exe`（Windows 上 CLI 会自动下载 NSIS，Linux 上需先 `apt install nsis`）。只想要绿色单文件可加 `-skip-installer`。

## CI / CD

`.github/workflows/windows.yml` 每次运行都从 `mygo.json` 读取 `name` / `slug` / `version` / `out`，再按触发事件自适应：

| 触发方式 | 行为 |
| --- | --- |
| Pull Request | `go test` / `go vet` / `gofmt`，只构建便携版 exe（跳过 NSIS，快速反馈） |
| push 到 `master` / `main` | 全量检查，构建 amd64 + arm64 的 exe 与 NSIS 安装包，上传 Actions 工件 |
| push `v*` 标签 | 同上，并自动创建 GitHub Release，附带 exe、安装包、portable zip 和 `SHA256SUMS.txt` |
| 手动运行 | 可自定义目标平台、是否打安装包、是否发布 Release |

版本必须与 `mygo.json` 一致（标签须为 `v<version>`），否则流水线直接报错；CI 还会通过 `MYGO_LDFLAGS=-X main.appVersion=...` 把配置里的版本注入二进制，避免代码里的版本号与配置漂移。构建矩阵由目标平台列表动态生成，新增架构不需要改 workflow。

发版流程：

```bash
# 1. 修改 mygo.json 里的 version（例如 5.0.4）
git tag v5.0.4
git push origin v5.0.4
```

也可以到 Actions 页选择 **Windows CI/CD → Run workflow**，手动指定 `windows/amd64` 等目标并强制发布。
