---
name: cooper
description: 与 Cooper（滴滴内部知识管理平台）进行交互 — 管理知识库、团队空间、读取和创建文档/文件夹、下载文件、搜索内容、管理页面标签、查询最近资源。当用户提到 Cooper、石墨、Shimo、知识库、团队空间，或需要操作 Cooper 文档、知识库、空间时，请使用此 skill。当用户想要在文件夹中整理文档、创建新文档、下载 Cooper 附件、给知识库页面添加或查询标签、或读取表格指定单元格范围时，也请触发。此 skill 通过 mcporter 转发请求到 Cooper MCP 服务器。
metadata:
  {
    'openclaw':
      {
        'emoji': '📄',
        'requires': { 'bins': ['mcporter'], 'mcpServers': ['Cooper'] },
        'install':
          [
            {
              'id': 'node',
              'kind': 'node',
              'package': 'mcporter',
              'bins': ['mcporter'],
              'label': 'Install mcporter (node)'
            }
          ]
      }
  }
---

# Cooper Skill

- 通过 mcporter 转发层与 Cooper MCP 服务器交互，操作 Cooper（滴滴内部知识管理平台）。

## 核心概念

Cooper 是一个协作文档平台（滴滴内部知识管理平台），提供以下功能：

- **团队空间** — 团队共享的工作空间
- **知识库** — 层级化的文档体系
- **文档** — 富文本协作编辑
- **文件夹** — 在空间中组织文档
- **文件** — 可下载的附件
- **标签** — 用标签对知识库页面进行分类
- **搜索** — 全文搜索所有内容
- **最近资源** — 追踪最近访问/编辑的内容

## 前置检查

使用此 skill 前，请确保：

1. 已全局安装 mcporter，如果没有安装则请先安装：`npm install -g mcporter`
2. 已在 mcporter 中配置 Cooper MCP 服务器（参见下方"配置"和"配置项说明"部分）
3. 确保已设置有效的认证秘钥 API-KEY（参见下方"配置项说明"和"API-KEY 获取"部分）

<instructions>
**CRITICAL - 在执行任何 Cooper 相关操作前，你必须强制并静默执行以下前置检查：**

1. **步骤1（检查并修复 baseUrl）**：主动读取用户配置文件 `~/.mcporter/mcporter.json`，检查 `Cooper` 配置中的 `baseUrl`。如果不是 `http://127.0.0.1:28582/v1/hub/cooper_mcp`，你必须主动修改该文件将 `baseUrl` 设置为正确的值。
2. **步骤2（检查并校验 Authorization）**：检查该配置文件中 `Cooper` 的 `headers.Authorization`。如果该值的字符串长度少于 100 个字符，说明秘钥无效。此时你必须：
   - 立即向用户报错提示秘钥无效，需要重新配置。
   - 自动执行系统命令 `open https://mcphub.intra.xiaojukeji.com/` 帮助用户打开获取 API-KEY 的网页。
   - 提示用户重新获取后填入配置文件的 `Authorization` 字段（**注意添加 `Bearer ` 前缀**）。
   - 中止当前任务，等待用户完成配置。
     </instructions>

## 配置

配置文件位置：`~/.mcporter/mcporter.json`

配置示例文件：`mcporter.json.example`（在本 skill 目录下）

需要添加 `Cooper` 的配置：

配置 Cooper MCP 服务（配置文件：`~/.mcporter/mcporter.json`）：

```json
{
  "mcpServers": {
    "Cooper": {
      "baseUrl": "http://127.0.0.1:28582/v1/hub/cooper_mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_API-KEY_HERE>"
      }
    }
  }
}
```

### 配置项说明

| 字段                    | 说明                                                                                                                             |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `baseUrl`               | Cooper MCP 服务器地址，值必须为 `http://127.0.0.1:28582/v1/hub/cooper_mcp`，如果不是则直接修改该配置文件                         |
| `headers.Authorization` | 认证 API-KEY，**格式必须为 `Bearer <API-KEY>`**，且API-KEY字符串长度至少100个字符，如果少于100个字符，则提示用户重新获取 API-KEY |

### API-KEY 获取

API-KEY 从 MCP Hub 获取：https://mcphub.intra.xiaojukeji.com/

1. 访问 MCP Hub 网站
2. 登录后获取你的 API-KEY
3. 将 API-KEY 填入配置文件的 `Authorization` 字段，**注意添加 `Bearer ` 前缀**

### 配置示例

```json
{
  "mcpServers": {
    "Cooper": {
      "baseUrl": "http://127.0.0.1:28582/v1/hub/cooper_mcp",
      "headers": {
        "Authorization": "Bearer your-API-KEY-from-mcphub"
      }
    }
  }
}
```

<instructions>
**CRITICAL - 在执行任何 Cooper 相关操作前，你必须强制并静默执行以下前置检查：**

1. **步骤1（检查并修复 baseUrl）**：主动读取用户配置文件 `~/.mcporter/mcporter.json`，检查 `Cooper` 配置中的 `baseUrl`。如果不是 `http://127.0.0.1:28582/v1/hub/cooper_mcp`，你必须主动修改该文件将 `baseUrl` 设置为正确的值。
2. **步骤2（检查并校验 Authorization）**：检查该配置文件中 `Cooper` 的 `headers.Authorization`。如果该值的字符串长度少于 100 个字符，说明秘钥无效。此时你必须：
   - 立即向用户报错提示秘钥无效，需要重新配置。
   - 自动执行系统命令 `open https://mcphub.intra.xiaojukeji.com/` 帮助用户打开获取 API-KEY 的网页。
   - 提示用户重新获取后填入配置文件的 `Authorization` 字段（**注意添加 `Bearer ` 前缀**）。
   - 中止当前任务，等待用户完成配置。
     </instructions>

验证配置：

```bash
mcporter list
mcporter config get Cooper
```

## 通过 mcporter 调用 Cooper 工具

所有 Cooper MCP 工具通过 mcporter 调用，格式如下：

```bash
mcporter call Cooper.<tool_name> <param1>=<value1> <param2>=<value2>
```

**重要提示**：始终使用 `Cooper.<tool_name>` 格式（不要使用 `mcp__Cooper__<tool_name>`）。mcporter CLI 会将 MCP 工具映射到 `Cooper.` 命名空间。

对于复杂参数（JSON 对象、数组或含空格的字符串），使用 `--args`：

```bash
mcporter call Cooper.<tool_name> --args '{"param1": "value1", "param2": ["a", "b"]}'
```

始终添加 `--output json` 以获取结构化结果：

```bash
mcporter call Cooper.<tool_name> <params> --output json
```

## Cooper MCP 工具参考

### 团队空间操作

#### 列出团队空间

```bash
# 列出我拥有的空间
mcporter call Cooper.listCooperSpaces type=1 --output json

# 列出我参与的空间
mcporter call Cooper.listCooperSpaces type=2 --output json

# type: 1 = 我拥有的, 2 = 我参与的
```

返回空间列表，包含每个空间的 `spaceId` 和 `spaceName`。

#### 获取空间目录结构

```bash
# 获取空间根目录
mcporter call Cooper.getCooperSpaceDirectory spaceId="<space_id>" parentId="0" --output json

# 获取子目录内容
mcporter call Cooper.getCooperSpaceDirectory spaceId="<space_id>" parentId="<folder_id>" --output json

# 个人空间使用 spaceId="0"
mcporter call Cooper.getCooperSpaceDirectory spaceId="0" parentId="0" --output json
```

- `spaceId`：空间 ID（个人空间用 `"0"`，团队空间用 `listCooperSpaces` 返回的 spaceId）
- `parentId`：父文件夹的 resourceId（根目录用 `"0"`）

#### 在空间中创建文件夹

```bash
# 在空间根目录创建文件夹
mcporter call Cooper.createCooperDirectory spaceId="<space_id>" name="新文件夹" parentId="0" --output json

# 在指定文件夹下创建子文件夹
mcporter call Cooper.createCooperDirectory spaceId="<space_id>" name="子文件夹" parentId="<parent_folder_id>" --output json
```

- `spaceId`：空间 ID（个人空间用 `"0"`）
- `name`：文件夹名称
- `parentId`：父目录的 resourceId（根目录用 `"0"`）

#### 在空间中创建文档

```bash
# 在空间根目录创建文档
mcporter call Cooper.createCooperDocument \
  spaceId="<space_id>" \
  parentId="0" \
  name="文档标题" \
  content="# 文档标题\n\n文档正文内容..." \
  --output json

# 在指定文件夹中创建文档
mcporter call Cooper.createCooperDocument \
  spaceId="<space_id>" \
  parentId="<folder_id>" \
  name="会议记录" \
  content="## 周会纪要\n\n- 讨论了项目进度\n- 下周计划..." \
  --output json
```

- `spaceId`：空间 ID（个人空间用 `"0"`）
- `parentId`：父目录的 resourceId（根目录用 `"0"`）
- `name`：文档标题
- `content`：文档内容，**markdown** 格式

#### 从空间下载文件

```bash
mcporter call Cooper.downloadCooperFile spaceId="<space_id>" resourceId="<file_id>" --output json
```

- `spaceId`：空间 ID（个人空间用 `"0"`）
- `resourceId`：文件的 resourceId（从目录列表中获取）

### 知识库操作

#### 列出知识库

```bash
# 列出所有知识库
mcporter call Cooper.listKnowledgeBases ownType=0 --output json

# ownType: 0=全部, 1=我拥有的, 2=转交给我的, 3=我参与的
```

#### 获取知识库目录结构

```bash
# 获取所有目录并展开指定父目录
mcporter call Cooper.getKnowledgeDirectory knowledgeId="<id>" parentResourceId="0" type=0 --output json

# 仅获取指定父目录下的子目录
mcporter call Cooper.getKnowledgeDirectory knowledgeId="<id>" parentResourceId="<parent>" type=1 --output json
```

#### 创建知识库页面

```bash
# 在知识库中创建新页面（markdown 内容）
mcporter call Cooper.createKnowledgePage \
  spaceId="<space_id>" \
  name="页面标题" \
  parentId="0" \
  content="## 章节\n\n页面内容，支持 **markdown** 语法..." \
  --output json
```

`createKnowledgePage` 接受 `contentType` 参数：使用 `"markdown"`（推荐）或 `"html"`。

### 文档操作

#### 读取文档内容

```bash
# 读取 Cooper 文档（appId=2）
mcporter call Cooper.readContent resourceId=<doc_id> appId=2 range="" --output json

# 读取知识库文档（appId=4）
mcporter call Cooper.readContent resourceId=<doc_id> appId=4 range="" --output json

# 读取表格单元格范围（仅对表格类型文档有效）
mcporter call Cooper.readContent resourceId=<sheet_id> appId=2 range="Sheet1!A1:D10" --output json
```

- `resourceId`：文档 ID
- `appId`：`2` 表示 Cooper 文档，`4` 表示知识库文档
- `range`：可选，仅对表格类型有效。格式：`SheetName!A1:B2`

### 标签管理（仅知识库页面）

标签管理功能可以让你用自定义标签来组织知识库页面，帮助分类和筛选内容。

#### 查询知识库标签列表

```bash
# 列出知识库中所有已有标签
mcporter call Cooper.getKnowledgeBaseTags knowledgeId="<kb_id>" --output json
```

返回知识库中定义的所有标签，包含标签 ID 和名称。在给页面分配标签之前，先用这个工具查看有哪些可用标签。

#### 查询页面标签

```bash
# 查询指定知识库页面的标签
mcporter call Cooper.getPageTags pageId="<page_id>" --output json
```

返回页面的标签，包括：

- **SYSTEM_SECURE** 标签 — 数据安全等级标签（系统自动分配）
- **CUSTOM** 标签 — 用户自定义标签

#### 给页面添加标签

```bash
# 添加已有标签到页面（标签已在知识库中存在）
mcporter call Cooper.addPageTag pageId="<page_id>" tagId="<tag_id>" tagName="<tag_name>" --output json

# 添加新标签到页面（标签不存在，会自动创建）
mcporter call Cooper.addPageTag pageId="<page_id>" tagId="" tagName="新标签名" --output json
```

- `pageId`：知识库页面资源 ID
- `tagId`：已有标签的 ID（新标签传空字符串 `""`）
- `tagName`：标签名称。当标签在知识库中不存在时，传 `tagId=""` 会自动创建新标签

### 搜索与发现

#### 搜索内容

```bash
mcporter call Cooper.search key="<search_term>" --output json
```

按关键词搜索所有用户文档。

#### 查询最近资源

```bash
# 查询类型：
# 1 = 最近访问, 2 = 最近编辑, 3 = 分享给我的
# 4 = 最近评论互动, 5 = 最近 DC 消息发给我的

# 最近文档
mcporter call Cooper.listRecent queryType=1 pageNum=0 pageSize=33 type="all" --output json

# 按资源类型过滤：coo_doc, coo_sheet, flow, mind, coo_ppt, coo_file, all
mcporter call Cooper.listRecent queryType=2 pageNum=0 pageSize=20 type="coo_doc" --output json

# 仅查询最近表格
mcporter call Cooper.listRecent queryType=1 pageNum=0 pageSize=20 type="coo_sheet" --output json
```

## 常见工作流

### 工作流 1：浏览和阅读知识库

```bash
# 1. 列出可用的知识库
mcporter call Cooper.listKnowledgeBases ownType=1 --output json

# 2. 获取目录结构
mcporter call Cooper.getKnowledgeDirectory knowledgeId="<kb_id>" parentResourceId="0" type=0 --output json

# 3. 读取文档内容
mcporter call Cooper.readContent resourceId=<page_id> appId=4 range="" --output json
```

### 工作流 2：搜索并导航

```bash
# 1. 搜索内容
mcporter call Cooper.search key="项目更新" --output json

# 2. 查看最近编辑
mcporter call Cooper.listRecent queryType=2 pageNum=0 pageSize=20 type="coo_doc" --output json

# 3. 读取找到的文档
mcporter call Cooper.readContent resourceId=<doc_id> appId=2 range="" --output json
```

### 工作流 3：创建知识库页面

```bash
# 1. 获取父目录（顶层页面使用根目录 "0"）
mcporter call Cooper.getKnowledgeDirectory knowledgeId="<space_id>" parentResourceId="0" type=1 --output json

# 2. 创建页面
mcporter call Cooper.createKnowledgePage \
  spaceId="<space_id>" \
  name="会议记录" \
  parentId="0" \
  content="## 会议记录\n\n讨论要点..." \
  --output json
```

### 工作流 4：管理团队空间

```bash
# 1. 列出我的团队空间
mcporter call Cooper.listCooperSpaces type=1 --output json

# 2. 浏览空间内容
mcporter call Cooper.getCooperSpaceDirectory spaceId="<space_id>" parentId="0" --output json

# 3. 创建文件夹进行分类整理
mcporter call Cooper.createCooperDirectory spaceId="<space_id>" name="项目文档" parentId="0" --output json

# 4. 在新文件夹中创建文档
mcporter call Cooper.createCooperDocument \
  spaceId="<space_id>" \
  parentId="<folder_id>" \
  name="需求文档" \
  content="# 需求文档\n\n## 背景\n\n## 功能描述" \
  --output json
```

### 工作流 5：下载文件

```bash
# 1. 浏览空间查找文件
mcporter call Cooper.getCooperSpaceDirectory spaceId="<space_id>" parentId="0" --output json

# 2. 下载文件
mcporter call Cooper.downloadCooperFile spaceId="<space_id>" resourceId="<file_id>" --output json
```

### 工作流 6：读取表格数据

```bash
# 1. 查找表格
mcporter call Cooper.search key="数据报表" --output json

# 2. 读取指定单元格范围
mcporter call Cooper.readContent resourceId=<sheet_id> appId=2 range="Sheet1!A1:F20" --output json
```

### 工作流 7：整理个人空间

```bash
# 1. 浏览个人空间根目录
mcporter call Cooper.getCooperSpaceDirectory spaceId="0" parentId="0" --output json

# 2. 创建文件夹结构
mcporter call Cooper.createCooperDirectory spaceId="0" name="2026年工作" parentId="0" --output json

# 3. 在文件夹中创建文档
mcporter call Cooper.createCooperDocument \
  spaceId="0" \
  parentId="<folder_id>" \
  name="Q1总结" \
  content="# Q1 工作总结\n\n## 完成事项\n\n## 待改进" \
  --output json
```

### 工作流 8：管理知识库页面标签

```bash
# 1. 查看知识库中已有的标签
mcporter call Cooper.getKnowledgeBaseTags knowledgeId="<kb_id>" --output json

# 2. 查看页面当前的标签
mcporter call Cooper.getPageTags pageId="<page_id>" --output json

# 3. 添加已有标签到页面
mcporter call Cooper.addPageTag pageId="<page_id>" tagId="<tag_id>" tagName="技术文档" --output json

# 4. 或者添加全新标签（自动创建）
mcporter call Cooper.addPageTag pageId="<page_id>" tagId="" tagName="Q2规划" --output json
```

## 参数说明

### 资源 ID

- `resourceId`：文档/文件的数字或字符串 ID
- `knowledgeId`/`spaceId`：知识库或空间标识符
- `parentResourceId`：目录/页面的父级（根目录用 `"0"`）

### 空间 ID 约定

- `"0"` — 个人空间
- `listCooperSpaces` 返回的其他值 — 团队空间

### 内容格式

- `createCooperDocument.content`：Markdown 格式
- `createKnowledgePage.content`：Markdown（推荐）或 HTML
- `createKnowledgePage.contentType`：`"markdown"` 或 `"html"`

### 标签参数

- `tagId`：从 `getKnowledgeBaseTags` 获取的已有标签 ID，或 `""`（空字符串）表示自动创建新标签
- `tagName`：标签的显示名称（当 `tagId` 为空时用于自动创建）
- 标签仅适用于知识库页面，不适用于 Cooper 空间文档

### 表格范围

- 格式：`SheetName!A1:B2`（例如 `Sheet1!A1:D10`）
- 仅在使用 `readContent` 读取表格类型文档时有效

## 错误处理

如果调用失败：

1. 检查 Cooper MCP 服务器是否运行：`mcporter list`
2. 验证认证：`mcporter config get Cooper`
3. 检查参数类型（ID 应匹配预期的格式）
4. 如果是 401/403 错误，API-KEY 可能已过期 — 从 MCP Hub 重新获取

## 使用技巧

- 所有调用都使用 `--output json` 以获取结构化数据
- 不确定时，先列出（`listKnowledgeBases`、`getCooperSpaceDirectory`、`listCooperSpaces`）再浏览
- 读取文档时，使用正确的 `appId`（Cooper 文档用 2，知识库用 4）
- 使用 `range` 参数读取表格中指定的单元格范围
- 搜索功能很强大，可以跨所有资源查找内容
- 查看最近资源可以帮助追踪活动和查找进行中的内容
- `createCooperDocument` 的内容是 markdown 格式 — 可以自然地使用标题、列表、加粗等
- 创建文件夹时要按步骤来：先创建父文件夹，然后用返回的 ID 创建子项
- 管理标签时，先用 `getKnowledgeBaseTags` 查看已有标签，避免重复创建

