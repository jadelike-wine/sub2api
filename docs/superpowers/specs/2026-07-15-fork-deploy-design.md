# Fork 部署方案定制化设计

- **Date**: 2026-07-15
- **Goal**: 把 fork 的 sub2api 项目从 `Wei-Shaw/sub2api` 切换为自己的 `jadelike-wine/sub2api`，保留全部自动化部署能力（裸机脚本一键安装 + GoReleaser 多平台打包 + Docker 镜像推送）
- **Approach**: 方案 A — 最小化改动，只改必须的引用

## 背景

sub2api 是 AI API Gateway 平台，支持多种部署路径：
- **裸机一键脚本**：`curl -sSL <raw install.sh url> | sudo bash`
- **GoReleaser CI**：push tag 自动构建多平台二进制 + 创建 Draft Release
- **Docker**：从 GHCR 拉镜像运行

安装脚本从当前 GitHub repo 的 releases API 拉取元数据和下载二进制。fork 后所有下载源还在 `Wei-Shaw/sub2api`，需要指向 `jadelike-wine/sub2api`。

## 影响范围分析

| 组件 | 位置 | 是否含 `Wei-Shaw` 硬编码 | 是否需要改 |
|------|------|--------------------------|-----------|
| install.sh 下载源 | `deploy/install.sh:34` | ✅ `GITHUB_REPO="Wei-Shaw/sub2api"` | ✅ **改** |
| README 一键命令 | `README*.md` | ✅ 含 install.sh 的 raw URL | ✅ **改** |
| GoReleaser 配置 | `.goreleaser.yaml`, `.goreleaser.simple.yaml` | ❌ 通过 `GITHUB_REPO_OWNER_LOWER` 动态获取 | ❌ 不动 |
| GitHub Actions release | `.github/workflows/release.yml` | ❌ 使用 `github.repository` | ❌ 不动 |
| Docker 镜像名 | goreleaser 模板 | ❌ 自动跟随仓库 owner | ❌ 不动 |
| systemd 服务文件 | `sub2api.service`（脚本内生成） | ⚠️ `Documentation=` 字段里指向原项目链接 | ❌ 文档链接，不动 |
| CLA workflow | `.github/workflows/cla.yml` | ✅ 有门控 `github.repository == 'Wei-Shaw/sub2api'` | ❌ 不会在 fork 中触发，不动 |

## 改动的文件清单

### 1. `deploy/install.sh`（1 处）

```bash
# 第 34 行
- GITHUB_REPO="Wei-Shaw/sub2api"
+ GITHUB_REPO="jadelike-wine/sub2api"
```

这是脚本访问 GitHub API（`/repos/{GITHUB_REPO}/releases`）和 releases 下载 URL 的唯一来源，改一处即可让整个脚本指向你的仓库。

### 2. README 文件（替换一键安装命令）

需要改的文件：
- `README.md`（英文）
- `README_CN.md`（中文）
- `README_JA.md`（日文）

每份文件中找到一键安装命令块，替换：

```markdown
# 替换前
curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/install.sh | sudo bash

# 替换后
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash
```

**注意**：仅替换一键安装命令这一个位置。README 中的其他外链（如 wiki、issue 链接、upstream 致谢链接）保持不动。

### 3. 可选：CLA workflow 清理

`cla.yml` 有 `github.repository == 'Wei-Shaw/sub2api'` 门控，在你的仓库里永远不会触发。可以删除也可以保留不动。此为纯 cleanup，不影响部署能力。

## 不变的部分（经代码扫描确认）

- `.github/workflows/release.yml`：使用 `github.repository`，无需改动
- `.goreleaser.yaml` / `.goreleaser.simple.yaml`：镜像名用 `ghcr.io/{{ .Env.GITHUB_REPO_OWNER_LOWER }}/sub2api:...`，owner 自动跟随
- `Dockerfile`、`deploy/Dockerfile`：都没硬编码仓库名
- `deploy/docker-compose*.yml`：没有预设 image 名，用户自行填充
- `deploy/sub2api.service`：脚本内通过 heredoc 生成，fields 只有 Description/Documentation 引用旧 URL（仅 metadata，不影响功能）

## 实施后首秀流程

完成改动并 push 到 `main` 后：

```bash
# 1. 确认当前 commit 就是你想要的版本
git tag v0.1.0

# 2. 推送 tag 触发 CI
git push origin v0.1.0

# 3. 等待 GitHub Actions 构建完成（约 3–5 分钟）
#    → 会自动创建 Draft Release，包含 checksums.txt + 多平台 tar.gz

# 4. 到 https://github.com/jadelike-wine/sub2api/releases 检查 Assets
#    确认包含：sub2api_v0.1.0_linux_amd64.tar.gz, sub2api_v0.1.0_linux_arm64.tar.gz,
#              sub2api_v0.1.0_darwin_amd64.tar.gz, sub2api_v0.1.0_darwin_arm64.tar.gz, checksums.txt

# 5. 点 Publish 发布
```

**前置条件**：GitHub repo Settings → Actions → General → Workflow permissions 需要选 "Read and write permissions"（允许 Actions 创建 Release）。

## 验证清单

- [ ] 在新机器上执行 `curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash` 能正常安装
- [ ] 安装后 `systemctl status sub2api` 运行正常
- [ ] 浏览器打开 `http://<server>:8080` 进入设置向导
- [ ] `ghcr.io/jadelike-wine/sub2api:<tag>` 镜像可拉取
- [ ] 升级命令 `sudo /opt/sub2api/sub2api upgrade` 能正常工作（从同仓库拉新版本）

## 回滚策略

如果改坏或有合并冲突：
- install.sh 1 行改动，可以直接 revert
- README 改动是单纯的 URL 字符串替换，可回退
- 没有任何破坏性改动，所有变更均在 `deploy/` 和 README 文档层
