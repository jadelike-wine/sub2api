#  Fork 部署方案实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 sub2api fork 从 `Wei-Shaw/sub2api` 切换为自己仓库 `jadelike-wine/sub2api`，让用户能通过 `curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash` 一键安装你发布的版本。

**Architecture:** 最小化改动原则 — 只改必须的下载源引用（install.sh + docker-deploy.sh + GoReleaser Docker LABEL + README 命令），CI workflow 和 goreleaser 配置通过动态环境变量自动跟随，无需修改。

**Tech Stack:** Bash、GitHub Actions、GoReleaser、Docker、systemd

**Adversarial inputs:** 用户 curl install.sh 时的 URL、release tag 格式（v0.1.0）、系统架构（amd64/arm64）、操作系统（linux/darwin）、GitHub API 响应

**Spec:** `docs/superpowers/specs/2026-07-15-fork-deploy-design.md`

---

## Task 1: 修改 install.sh 的 GITHUB_REPO

**Files:**
- Modify: `deploy/install.sh:34`

- [ ] **Step 1: 替换安装脚本下载源**

把 `deploy/install.sh` 第 34 行从：

```bash
GITHUB_REPO="Wei-Shaw/sub2api"
```

改为：

```bash
GITHUB_REPO="jadelike-wine/sub2api"
```

- [ ] **Step 2: 同步更新 install.sh 顶部的用法注释**

把 `deploy/install.sh` 第 5 行从：

```
# Usage: curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/install.sh | bash
```

改为：

```
# Usage: curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | bash
```

- [ ] **Step 3: 提交**

```bash
git add deploy/install.sh
git commit -m "chore(deploy): point install.sh to jadelike-wine/sub2api

Update GITHUB_REPO and usage comment to use the forked repo
so the one-click install command resolves releases correctly."
```

---

## Task 2: 修改 docker-deploy.sh 的 GITHUB_RAW_URL

**Files:**
- Modify: `deploy/docker-deploy.sh:24`

- [ ] **Step 1: 替换 raw 文件源**

把 `deploy/docker-deploy.sh` 第 24 行从：

```bash
GITHUB_RAW_URL="https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy"
```

改为：

```bash
GITHUB_RAW_URL="https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy"
```

- [ ] **Step 2: 提交**

```bash
git add deploy/docker-deploy.sh
git commit -m "chore(deploy): point docker-deploy.sh to jadelike-wine/sub2api"
```

---

## Task 3: 更新 root README.md 中的一键安装命令

**Files:**
- Modify: `README.md`（英文）

- [ ] **Step 1: 替换一键裸机安装命令**

把 `README.md` 第 238 行：

```markdown
curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/install.sh | sudo bash
```

改为：

```markdown
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash
```

- [ ] **Step 2: 替换卸载命令**

把 `README.md` 第 288 行：

```markdown
curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/install.sh | sudo bash -s -- uninstall -y
```

改为：

```markdown
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash -s -- uninstall -y
```

- [ ] **Step 3: 替换一键 Docker 安装命令**

把 `README.md` 第 311 行：

```markdown
curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/docker-deploy.sh | bash
```

改为：

```markdown
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/docker-deploy.sh | bash
```

- [ ] **Step 4: 提交**

```bash
git add README.md
git commit -m "docs: update README.md one-click install URLs to jadelike-wine/sub2api"
```

---

## Task 4: 更新 README_CN.md 中的一键安装命令

**Files:**
- Modify: `README_CN.md`（中文）

- [ ] **Step 1: 替换裸机安装命令**

第 241 行：

```markdown
curl -sSL https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/install.sh | sudo bash
```

→

```markdown
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash
```

- [ ] **Step 2: 替换卸载命令**

第 291 行：`Wei-Shaw` → `jadelike-wine`（install.sh uninstall URL）

- [ ] **Step 3: 替换 Docker 安装命令**

第 314 行：`Wei-Shaw` → `jadelike-wine`（docker-deploy.sh URL）

- [ ] **Step 4: 提交**

```bash
git add README_CN.md
git commit -m "docs: update README_CN.md one-click install URLs to jadelike-wine/sub2api"
```

---

## Task 5: 更新 README_JA.md 中的一键安装命令

**Files:**
- Modify: `README_JA.md`（日文）

- [ ] **Step 1: 替换裸机安装命令**

第 236 行：`Wei-Shaw` → `jadelike-wine`

- [ ] **Step 2: 替换卸载命令**

第 286 行：`Wei-Shaw` → `jadelike-wine`

- [ ] **Step 3: 替换 Docker 安装命令**

第 309 行：`Wei-Shaw` → `jadelike-wine`

- [ ] **Step 4: 提交**

```bash
git add README_JA.md
git commit -m "docs: update README_JA.md one-click install URLs to jadelike-wine/sub2api"
```

---

## Task 6: 更新 deploy/README.md 中的一键命令

**Files:**
- Modify: `deploy/README.md`（部署文档）

- [ ] **Step 1: 替换一键安装/部署命令**

把 `deploy/README.md` 第 58 行和 61 行中的：

```
https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy/docker-deploy.sh
```

替换为：

```
https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/docker-deploy.sh
```

- [ ] **Step 2: 替换一行命令安装脚本**

第 377 行：`Wei-Shaw/sub2api` → `jadelike-wine/sub2api`

- [ ] **Step 3: 提交**

```bash
git add deploy/README.md
git commit -m "docs: update deploy/README.md URLs to jadelike-wine/sub2api"
```

---

## Task 7: 验证安装端到端（无需推 tag 即可测试）

- [ ] **Step 1: 确认 main 分支已接受所有改动**

```bash
git log --oneline -10
```

预期：能看到 Tasks 1–6 各自的 commit。

- [ ] **Step 2: raw 文件 URL 可达性检查**

```bash
curl -sI https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | head -5
curl -sI https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/docker-deploy.sh | head -5
```

预期：返回 `HTTP/2 200`，content-type 为 `text/plain`。

- [ ] **Step 3: 安装脚本内容确认**

```bash
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | grep -m1 "GITHUB_REPO"
```

预期输出：`GITHUB_REPO="jadelike-wine/sub2api"`

---

## Task 8: 首发 Release（打 tag 触发自动构建）

- [ ] **Step 1: 确认要发布的版本号**

约定与原项目一致，使用语义化版本 `v<major>.<minor>.<patch>`。首次建议 `v0.1.0` 或从你当前的功能起点取合适的版本号。

- [ ] **Step 2: 打 lightweight tag 并推送**

```bash
git tag v0.1.0
git push origin v0.1.0
```

- [ ] **Step 3: 观察 GitHub Actions 构建**

浏览 `https://github.com/jadelike-wine/sub2api/actions`，确认 "release" workflow 已成功触发并跑完（约 3–5 分钟）。

- [ ] **Step 4: 检查 Draft Release Assets**

浏览 `https://github.com/jadelike-wine/sub2api/releases`，确认 Draft Release 包含：

- `checksums.txt`
- `sub2api_<version>_linux_amd64.tar.gz`
- `sub2api_<version>_linux_arm64.tar.gz`
- `sub2api_<version>_darwin_amd64.tar.gz`
- `sub2api_<version>_darwin_arm64.tar.gz`
- （可选）`sub2api_<version>_windows_amd64.zip`

- [ ] **Step 5: Publish Release**

在 GitHub Release 页面检查 Draft 无误后点击 **Publish**。

---

## Task 9: 端到端验证——在干净机器上安装

- [ ] **Step 1: 在干净测试机/VM 上执行一键安装**

```bash
curl -sSL https://raw.githubusercontent.com/jadelike-wine/sub2api/main/deploy/install.sh | sudo bash
```

- [ ] **Step 2: 验证服务状态**

```bash
sudo systemctl status sub2api
```

预期：`active (running)`，日志无报错。

- [ ] **Step 3: 验证本地访问**

```bash
curl -s http://127.0.0.1:8080/health
```

预期：返回健康检查 JSON。

- [ ] **Step 4: 清理测试环境**

```bash
sudo /opt/sub2api/sub2api uninstall -y
```

然后用 Task 8 的方式重新发布后续版本，确认升级命令也工作：

```bash
sudo /opt/sub2api/sub2api upgrade
```

---

## 完成检查点

所有 tasks 完成后，对照 spec 核查：

- [ ] Task 1: `deploy/install.sh` 的 `GITHUB_REPO` 已改 → 覆盖 spec §"1. deploy/install.sh"
- [ ] Task 2: `deploy/docker-deploy.sh` 的 `GITHUB_RAW_URL` 已改 → Docker 部署路径
- [ ] Tasks 3–6: 所有 README 一键命令 URL 已改 → 覆盖 spec §"2. README 文件"
- [ ] Task 7: raw URL 可达 → 覆盖 spec §"验证清单"
- [ ] Task 8: 成功发布首个 Release → 覆盖 spec §"实施后首秀流程"
- [ ] Task 9: 端到端安装验证通过 → 覆盖 spec §"验证清单"

**不需要改的（已通过代码扫描确认）：**
- `.github/workflows/release.yml` — 使用 `github.repository` 动态获取
- `.goreleaser.yaml` / `.goreleaser.simple.yaml` — 镜像名通过 `GITHUB_REPO_OWNER_LOWER` 环境变量自动跟随
- `Dockerfile` — 无硬编码仓库名
- `deploy/docker-compose*.yml` — 无预设 image 名
- `.github/workflows/cla.yml` — 有 `github.repository == 'Wei-Shaw/sub2api'` 门控，不会在 fork 中触发
