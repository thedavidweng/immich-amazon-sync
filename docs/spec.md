# Spec: Immich → Amazon Photos 持续同步服务

> **状态**：Ready for agent（规格已完整定义；尚未发布到 GitHub Issue）  
> **日期**：2026-10-20  
> **拟建仓库**：[`thedavidweng/immich-amazon-sync`](https://github.com/thedavidweng/immich-amazon-sync)（检查时尚不存在，待创建）  
> **建议仓库描述**：`Self-hosted, reliable one-way sync from Immich to Amazon Photos, powered by aphotos. Docker-first, crash-safe, and deletion-safe.`  
> **实施者**：没有本次聊天上下文的 Coding Agent；本文为唯一需求基线，须先核对上游现状，再实施。  
> **规格方法**：[mattpocock/skills — to-spec](https://raw.githubusercontent.com/mattpocock/skills/refs/heads/main/skills/engineering/to-spec/SKILL.md)

## Problem Statement

用户已经维护两个相关的开源软件：

1. **Rainforest**：基于 Go 与 MyGo 的 Amazon Photos 桌面 App，具有本地文件的一次性上传、单向文件夹备份、SQLite 传输队列、启动后对账、恢复下载、区域化 Cookie 登录等能力。
2. **amazon-photos / aphotos**：独立的纯 Go Amazon Photos 非官方 SDK 和 CLI，具有区域路由、流式上传、MD5 验证、节点查询、相册操作与保护性错误处理。

用户另有一个尚未覆盖的实际场景：**使 Immich 的原始照片/视频及指定相册自动、持久、可靠地备份到 Amazon Photos**；典型部署位置为 NAS 或 Docker 主机，可用于 Echo Show 等仅从 Amazon Photos 取图的设备，也可作 Immich 之外的额外云端副本。用户不应被要求让桌面 App 全天保持开启，也不应为了基本同步自己编排 Shell 脚本、重复导入相同照片，或因服务重启和暂时性 API 故障遭受静默漏传/误删。

现成的 [`vanessa/immich-amazon-sync`](https://github.com/vanessa/immich-amazon-sync) 满足基础场景，但其代码以个人用途为边界：定时全量拉指定相册、以 JSON 保存 Immich Asset ID → Amazon Node ID 映射、主要在美国环境验证，并且**默认将从选定相册中移除的照片送入 Amazon 回收站**；上传实现会将整个原件读入内存，重启时未对上传中断的不确定提交状态作强保护。直接扩展或 Fork 它，相当于在维护第二套 Amazon HTTP 协议客户端。

[`ImAleeexx/immich-gphotos`](https://github.com/ImAleeexx/immich-gphotos) 的目标云服务为 Google Photos，无法直接复用上传协议，但可学习其**事件驱动 + Reconciler + Backfill + 持久化队列 + 独立相册关系 + 安全删除**等产品和可靠性设计。其 Google SHA-1 预去重不能直接移植到 Amazon 的 MD5 查询。

**待解决的真正问题是：在不破坏 Rainforest 现有桌面产品边界、也不复制 Amazon API 实现的前提下，为 Immich 建立可常驻、可恢复、可观测且默认不执行破坏性操作的同步入口。**

## Solution

建立第三个独立的 **Docker-first、纯 Go、headless 同步服务**，暂定仓库及可执行文件名为 `immich-amazon-sync`。其职责为从 Immich **单向**读取资产和相册关系，向 Amazon Photos 写入原始媒体及对应相册成员关系，维护本地 SQLite 操作状态。采用以下不可违反的依赖方向：

- **Rainforest** 保持桌面上传、文件夹备份、传输监控、恢复下载职责；不在 Rainforest 的桌面进程里运行服务器端 Immich 同步守护进程。
- **amazon-photos (`aphotos`)** 保持可复用的 Amazon API SDK/CLI；新的同步服务直接依赖其公开 Go SDK。通用 Amazon API 缺陷修复进入 SDK，Immich 专用编排和状态逻辑只进入新服务。
- **新同步服务** 单独部署、单独持久化状态、单独配置 Immich 和 Amazon 凭据，不导入 Rainforest 的私有 `internal` 包，也不共享其数据库/加密存储文件；Rainforest 不依赖新服务。
- **默认是非破坏性追加备份**：源端删除、移出相册、相册重命名、暂时失联，绝不自动删除 Amazon 媒体文件。未来可选择性删除保护功能应独立显式启用，不能是第一次发布的默认行为。
- 从 **最小可靠版本（P0）** 开始，实现 **事件 + 对账及相册镜像（P1）**，最终才增加 **轻量 Web 管理、多账户和受保护可选删除（P2）**。三阶段都属于此完成体愿景，逐步交付但避免超前的复杂性。

产品目标是复制**用户场景**而不是复制上游项目代码。不要改写 Rainforest 的 MyGo UI，不要再实现第二套 Amazon Drive v1 客户端，不要 fork Python 项目后把它作为内部子进程。

## User Stories

以下所有条目均采用“作为某类用户，我希望……，以便……”的可验证故事形式；标注 P0/P1/P2 是最低交付阶段，而非放弃后续阶段。

### 连接与首次配置

1. **[P0]** 作为 Immich 管理员，我希望提供 Immich 地址及最小权限 API Key，以便同步服务读取我的照片而不获得多余写权限。
2. **[P0]** 作为 Amazon Photos 用户，我希望输入区域和受保护的会话 Cookie，以便连接正确 Marketplace 而不是默默回退到美国区。
3. **[P0]** 作为使用加拿大、英国、日本或欧洲站点的用户，我希望沿用 SDK 的区域路由和能力探测，以便识别当前账户实际可用的读写能力。
4. **[P0]** 作为初次安装者，我希望先运行只读连接诊断，以便在上传任何媒体前发现无效凭据、权限或不受支持的 API 行为。
5. **[P0]** 作为 Docker 用户，我希望只需一个容器、一个持久卷和简单配置即可启动，以便在 NAS 上长期运行。
6. **[P0]** 作为安全敏感用户，我希望凭据不出现在镜像、日志、错误详情、Docker Compose 明文样例或健康检查响应中，以便减少意外泄露。
7. **[P1]** 作为低维护用户，我希望服务能明确报告 Cookie 已过期并在更新后继续工作，以便不用删除状态数据库重来。
8. **[P2]** 作为不熟悉 CLI 的用户，我希望通过本地受保护的管理页面完成首次配置，以便不编辑复杂配置文件。

### 同步目标与媒体行为

9. **[P0]** 作为 Immich 用户，我希望按稳定的相册 ID 明确选择需要备份的相册，以便不因重名或改名上传错误相册。
10. **[P0]** 作为备份用户，我希望将 Immich 原件而非缩略图或转码预览传至 Amazon，以便尽可能保留原始画质和元数据。
11. **[P0]** 作为照片与视频拥有者，我希望可以选择只同步照片或包含视频，以便控制容量和流量。
12. **[P0]** 作为拥有大量历史照片的用户，我希望首次扫描有进度及恢复点，以便中途重启不会从头重复上传。
13. **[P0]** 作为拥有已有 Amazon 照片的用户，我希望以经确认的文件校验信息发现相同内容，以便减少重复上传。
14. **[P0]** 作为上传大量大视频的用户，我希望传输不将整个文件载入 RAM，以便小内存 NAS 也可以运行。
15. **[P0]** 作为每天拍照的用户，我希望同一源资产被重复扫描时不会重复创建 Amazon 文件，以便任务可安全重试。
16. **[P0]** 作为使用自托管系统的用户，我希望限定并发、暂存磁盘和传输速率策略，以便服务不会挤占整个 NAS 的资源。
17. **[P1]** 作为用户，我希望 Immich 新增资产时快速入队，以便不用等待长周期的定时扫描。
18. **[P1]** 作为经历容器宕机的用户，我希望新照片仍在定期对账后补传，以便不会因为漏掉 Webhook 永久缺档。
19. **[P1]** 作为正在迁移几十万张照片的用户，我希望历史 Backfill 比新照片低优先级，以便当前照片不被旧库存堵塞。
20. **[P1]** 作为用户，我希望多个 Immich 相册可引用 Amazon 的同一媒体文件，以便不因为它属于两个相册就上传两份。
21. **[P1]** 作为用户，我希望指定的 Immich 相册在 Amazon 上形成对应相册，以便 Echo Show 或 Amazon Photos 能直接选用。
22. **[P1]** 作为用户，我希望源相册成员关系的新增和撤销能够同步到服务创建的对应 Amazon 相册，以便相册展示与源端一致，但不删除云端媒体本身。
23. **[P1]** 作为照片收藏者，我希望 Motion Photo、HEIC 和视频原件不会被不知情地拆分、降质或伪造支持，以便知道备份真实保留了什么。
24. **[P1]** 作为用户，我希望能够明确选择整个资料库而不仅是相册 allowlist，以便在理解规模后进行全库额外备份。

### 恢复、正确性与删除保护

25. **[P0]** 作为用户，我希望上传成功后但状态尚未来得及写盘就断电，重启仍先查证远端再重试，以便避免盲目重复上传。
26. **[P0]** 作为用户，我希望来源 API 暂时不可用时保留历史状态，以便不会把无法读取误判为文件已删除。
27. **[P0]** 作为用户，我希望系统区分“已确认上传”“已存在并验证”“上传结果不确定”“上传失败”，以便不产生假成功。
28. **[P0]** 作为用户，我希望把不存在或访问不到的相册标记为错误且暂停相应工作，以便改名、移除或权限变化不会触发破坏性动作。
29. **[P0]** 作为用户，我希望默认仅新增或修复副本、不回传删除，以便 Immich 中的整理动作不会清理 Amazon 的备份。
30. **[P1]** 作为用户，我希望即使已上传媒体但相册关联失败，也只补相册关联而不重传原件，以便避免重复文件。
31. **[P1]** 作为用户，我希望源端某次分页漏记录、结果为空或游标异常时不推进已完成进度，以便故障后可重复对账。
32. **[P2]** 作为主动选择删除传播的高级用户，我希望先获得差异预览、类型化确认、硬性批量删除阈值与独立开关，以便避免误删整库。
33. **[P2]** 作为撤销相册同步的用户，我希望默认仅停用规则和停止维护关系，而不是删除此前上传的 Amazon 媒体，以便保留备份。

### 运行与维护

34. **[P0]** 作为运维者，我希望拥有 health/readiness、结构化日志、可机器处理的诊断输出和确定性的退出码，以便排查问题和监控容器。
35. **[P0]** 作为运维者，我希望能优雅终止并正确重启同步服务，以便镜像升级不会造成丢失进度。
36. **[P0]** 作为贡献者，我希望不用真实 Immich/Amazon 账户就能运行完整自动化测试，以便安全地开发和审查 PR。
37. **[P0]** 作为 NAS 用户，我希望同时提供 linux/amd64 和 linux/arm64 Docker 镜像，以便在 Intel 与 ARM 设备部署。
38. **[P1]** 作为贡献者，我希望兼容性通过上游 Immich OpenAPI 契约测试及时暴露，以便 API 改版不导致静默错误。
39. **[P2]** 作为拥有家庭多个用户的管理员，我希望多 Immich 用户/多 Amazon 账户之间完全隔离同步状态，以便个人照片不串到别人的账户。
40. **[P2]** 作为管理员，我希望查看 Backfill、队列、失败项和最后一次成功对账，并暂停或重试，以便无需依赖日志管理日常同步。
41. **[P2]** 作为不想暴露私密 API 的用户，我希望管理页面默认只在本机或可信内网开放并需要认证，以便不能被未授权访问。
42. **[P0–P2]** 作为开源维护者，我希望兼容性、限制、错误恢复及非官方 Amazon API 风险在文档中说清，以便使用者作知情决定。

## Implementation Decisions

### A. 产品和仓库边界（既有对话已确认）

1. **建立独立仓库**，不在 `rainforest` 仓库增加 Immich Sync 页面、系统服务或 Docker daemon；Rainforest 后续最多作为远程管理客户端，是另一份独立提案，当前不做。
2. **单一语言 Go**，尽可能纯 Go、`CGO_ENABLED=0`；用现有 `aphotos` Go SDK 访问 Amazon Photos，不通过 CLI 子进程调用，不手写 Drive v1 路由，也不引入 Python 服务。
3. **Docker-first、headless-first**：可本地直接运行单一二进制，但发布和文档首先服务于长驻 Docker/Compose/NAS 用户；服务不是 Immich 的插件，也不是修改 Immich 容器的补丁。
4. **继续独立维护三份职责**：SDK 只解决 Amazon API；新项目实现 Immich 客户端、同步策略、状态和调度；Rainforest 只解决桌面本地文件工作流。
5. 复制参考项目的**行为要求与可靠性思路**，不直接移植其 Python 代码；不引入 Google Photos 的私有 API、Google 认证或无限容量承诺。
6. 新项目是**单向备份与相册镜像**，不是可双向传播修改/删除的通用同步引擎。默认非破坏性原则优先于“完全镜像”字面语义。
7. 默认部署**单实例单写者**；不支持把同一 SQLite 库挂给多个运行中的容器。对并发实例争锁失败应退出并给出明确诊断，而不是发生竞态。

### B. SDK 复用与接口契约

8. Amazon 端通过 SDK 公共 `Client` 抽象，复用区域信息、规范化 Cookie、`Search/QueryNodes`、`GetNode`、`Upload`、`CreateAlbum`、`AddAlbumMembers`、`RemoveAlbumMembers` 和 SDK 的 `Redact`。SDK 缺功能时先评估是否是**通用**能力，只有通用问题才向 SDK 提独立 PR；不要导入 Rainforest 私有核心。
9. SDK 已实现 `io.Reader` 流式上传和确认/未确认传输语义。新服务在需要重试和预先校验时，应以**有界磁盘暂存**提供可重新读取的原件，并在实际发起上传之前持久保存待上传文件的 MD5/字节数/资产身份，作为失败后的对账依据，禁止 `ReadAll` 整部视频或将整文件构造为内存 multipart；磁盘容量不足必须显式报错、可恢复。
10. **Immich SHA-1 不等于 Amazon MD5**。对原件流式读取/暂存时计算 MD5；可额外核对 Immich 提供的 SHA-1 与原始字节一致性。Amazon 去重只能依靠实际被目标支持且已验证的 MD5/大小/目标父级/身份等信息，不得直接将 Immich SHA-1 当作可用于 Amazon 的哈希。
11. 对已存在文件的“跳过”必须依赖远端确认的节点及匹配内容校验，不能仅因同名、HTTP 409 或本地 Immich ID 便假设 Amazon 上已有相同文件。远端缺乏足够校验字段时状态为**未确认/需人工诊断**，不得自动判定成功；如果出现冲突，按可测试的安全策略处理。
12. 上传操作存在**提交成功、响应丢失**的时序风险；任务在 `unconfirmed` 时不得直接重试 POST，而应按既有可用的 MD5/文件名/已知目标进行远端对账，成功后恢复 Node ID。无法证明是否提交时保持未确认并报告，不通过任意重命名掩盖疑似重复。
13. 区域由 SDK 官方路由表决定；严格区分 `verified` 与 `experimental`。非美区先进行只读探测，并在允许写入的明确操作前做能力诊断；不能把非美区静态一刀切封禁，也不能因实验性标记就认定写入一定成功。只有真实端到端验证才可升级“verified”证据。
14. Amazon 会话采用 Cookie 而不是虚构的 OAuth/refresh token：会过期、需要使用者重新提供，服务在 401/403 时暂停相关账户写入并保留全部状态；不得自动穷举或绕过 Amazon 登录挑战。

### C. Immich 数据源和范围

15. `ImmichClient` 是单独的边界接口，能力包括版本/权限探测、列出可见相册、按相册 ID 分页列资产、按更新时间增量搜索、下载原始二进制流、读取稳定资产身份及元数据；只读权限为默认，除 P1 用户选择自动注册 Workflow 外不要求额外写权限。
16. API 合同以 Immich 官方 **OpenAPI** 为权威，采用运行时 capability/version probe，不能硬编码预览版接口为所有部署都可用，也不能盲目承诺在老版本有 Workflow；缺少 Workflow 权限/能力时自动退回纯轮询，不影响正确性。
17. 以 **Immich user/account identity + Asset ID + Album ID** 作为远端身份；相册以 ID 而不是名字选择与映射。相册重命名保持关联不变，相册不存在/访问失败是故障而非空相册；多个相册引用同一 Asset ID 时只上传一次媒体。
18. P0 默认必须选择**至少一个明确的源相册 ID**，同步对象为这些相册的图片/视频；P1 增加经过用户显式选择的**整个可访问资料库**模式。两种模式的边界在配置与日志中必须清晰，不能在版本升级时悄悄扩大同步范围。
19. 下载 Immich 原始文件时仅使用官方 Original 下载路径/语义，不读取 Immich 内部 PostgreSQL、不假设与 Immich 有共享文件系统、不使用缩略图或转码预览代替原件；若将来提供只读磁盘挂载优化，须验证路径安全与同一原件身份后另立任务。
20. 默认保留媒体原始文件名与可用拍摄时间元数据，不伪造 EXIF、时区、Live/Motion Photo 的目标端展示能力；对 Immich 隐藏派生短视频等特殊媒体应做显式可测的过滤政策，避免同一 Motion Photo 意外拆成重复视频，但不得删除用户真实独立的视频。
21. 支持的媒体类型以 Immich 原始 MIME、Amazon SDK 实际可上传能力及已验证清单交叉决定。不支持格式保留明确状态和原因，视频开关默认由用户配置，不能偷偷转换成低质量版本。

### D. 持久化与同步状态

22. 采用纯 Go SQLite（优先与 Rainforest 一致的 `modernc.org/sqlite`）作为**唯一持久化业务状态真相**；不要 JSON 映射文件与数据库双写，也不建消息队列/Redis/PostgreSQL 的额外服务。使用事务、唯一索引、迁移版本、WAL、外键与合理 busy timeout。
23. 状态至少逻辑上包括：同步账户（含身份，不含未保护的明文敏感日志）；配置的相册/资料库规则；Immich Asset 发现记录；内容与 Amazon Node 的已验证映射；待处理/处理中/未确认/失败/完成的任务；源相册与 Amazon 相册映射；媒体与相册成员关系；每次扫描/Backfill 游标；最近错误与运行统计。
24. 各账户与规则应有稳定唯一约束；相同 `(账户, Immich Asset ID)` 多次到达仅归并为同一工作项。内容级去重不以相册成员关系为边界，已确认 `Amazon Node ID` 可以被该账户多个受管理相册引用。
25. **关键状态必须区分**：发现/待处理、暂存完成、传输中、结果未确认、云端媒体已确认、相册关联待办、完全完成、可重试错误、阻塞/需操作。状态名是概念契约，不强制数据库使用一模一样的枚举文本。
26. 媒体上传和加入相册是**两个独立可重试阶段**；已确认 Node ID 在数据库事务中提交，后续关联失败不得回退到未上传状态。对账也不允许创建文件夹或相册等额外写入副作用。
27. 异常重启后重新分配长时间 `running` 的任务，但必须首先检查远端是否已经提交。扫描完成/更新时间游标仅在分页**全部成功且状态入库**后推进；使用重叠窗口去重以处理边界更新和事件乱序。分页回退、重复页或永不前进必须显式报错。
28. 历史 Backfill 与增量 Reconcile 分离；每次处理有限页/有限时间，不阻塞实时高优先级任务。Webhook **只是加速信号，不是唯一真相**；所有 Webhook 漏失必须最终由定期重新读取 Immich 恢复。
29. 相册成员增量对账必须区分“移出相册”与“删除媒体”。若 P1 开启相册镜像，允许把成员从服务**拥有且映射的 Amazon 目标相册**移除，但**永远不因此 MoveToTrash Amazon 文件**；多个源相册共享同一媒体时，单个成员撤销不能影响其他相册。
30. 对源端不完整数据采取 fail-closed 策略：HTTP 错误、401/403、空分页异常、相册不见、数据库不可读、游标异常或临时离线，都不能触发远端媒体删除或清空本地映射。

### E. 调度、资源与可靠性

31. P0 先实现可靠**周期扫描**，默认每 15 分钟检查增量，启用新配置时执行有进度的历史扫描；P1 引入 Immich Workflow 的 AssetCreate → Webhook 事件，并保留 15 分钟 Reconciler 兜底。默认值可作为性能测试后的可配置参数，不可小于可控限流机制。
32. P1 Webhook 接收端必须验证不可预测的共享 Secret，快速将事件持久化去重后返回；不在请求线程执行上传、不盲信输入的媒体 URL 或任意文件路径；Webhook 未启用/未配置或无法从 Immich 访问时可始终轮询。
33. 对 HTTP 429、5xx、临时网络失败采用有上限的指数退避与 jitter，尊重明确的 Retry-After；认证失败进入需人工处理的暂停状态，不能无限重试刷接口。
34. 提供低内存设计与资源限制：默认较小并发（例如两个上传 Worker、一个低优先级 Backfill Worker），最大临时文件数和字节数可配置，临时文件写盘并校验完成后才能用于上传；下线/中断时可识别并清理**孤儿临时文件**，不删除仍被队列引用的暂存件。
35. 提供 graceful shutdown：停止获取新任务、在有限宽限期内结束或取消当前 HTTP 操作、将中断工作保留为待对账状态，干净关闭 DB。优先交付正确性而不是最高传输吞吐。
36. 只使用单容器部署；进程异常自动重启、持久卷、最小权限运行用户、Docker HEALTHCHECK/HTTP 探针，不能把带 Cookie 的数据卷无保护地暴露给其他容器。
37. 对“没有配置源相册”“无凭据”“凭据过期”“磁盘暂存不足”“Immich 暂时不可用”“Amazon 限流”“Amazon 上传不确定”输出不同机器可理解的诊断类别和人类可读信息，且统一脱敏。

### F. CLI、配置和可观测性

38. 初期提供标准化 CLI 入口：`serve`（常驻）、`sync once`（一次受控扫描/补传）、`albums list`（列出当前 API Key 可访问的相册 ID、名称和资产数量以供配置）、`doctor`（只读环境与账户诊断）、`version`；使用 Cobra 而不是自行手写多级参数解析，遵循显式 flags/help、可脚本化退出码，并保留与 Rainforest/aphotos 风格相近的 `--json` 诊断模式。
39. P0 首次配置使用环境变量加**挂载的只读凭据文件或 Docker secret 文件**；推荐分离非机密配置、Immich API Key 与 Amazon Cookie，禁止要求在 Compose 示例的 `environment:` 块放真实密钥。环境变量直传 Cookie 可以作为兼容途径，但文档必须说明其暴露风险。
40. 最小配置语义包括：Immich API URL/Key、Amazon 区域/认证凭据、源相册 ID allowlist、照片/视频选项、同步间隔、并发和暂存上限、持久数据目录、诊断日志级别。提供无密钥的示例配置、参数默认值和无歧义错误反馈。禁止默认到错误的 Amazon 区域或悄悄开启全库扫描。
41. 提供 `/healthz`（进程运行）、`/readyz`（核心本地服务可工作；远端不可用以诊断状态呈现，避免重启风暴）；P1 增加事件入口。管理类 HTTP 端点不得默认公网暴露，示例仅绑定 localhost 或私有 Docker 网络，凭据不允许通过探针返回。
42. 结构化日志至少包含账户匿名标识、规则、Job ID、阶段、已处理/待处理计数、耗时、有限错误码、最后成功对账时间；不得包含原始 Cookie、API Key、Immich 私人资产 URL、预签名 URL 或可识别照片内容。无需额外部署 Prometheus/OTel 服务才能运行。
43. P2 Web 管理 UI 是**可选附加前端**，不改变无界面服务内核；设置、相册选择、同步概览、队列、错误、诊断、暂停/恢复有明确权限和 CSRF/session 保护。不要为了 UI 给 P0 引入大型前端工具链。
44. P2 多账户按账户独立 Cookie/Region、Immich API Key、游标、队列和 Amazon Node 映射；任何跨账户去重和媒体复用都禁止，单个账户失效不得阻塞其他账户。只支持一台容器的本地管理，分布式调度不做。

### G. 删除语义与安全政策

45. **P0、P1 永远不调用 Amazon MoveToTrash**。源端删除、相册被移除、规则取消、全库模式关闭、重建数据库、回滚 Immich 快照均不应自动删除 Amazon 媒体。
46. P1 对由服务创建/明确管理的 Amazon 相册可做**仅成员关系**的有边界对账；在首次扫描未完全成功、源相册丢失、批量差异异常或未确认目标相册所有权时不得执行任何成员移除。
47. P2 可选的媒体删除传播不是隐式升级：必须分别启用、进行明确确认、提供 dry-run 清单，并以硬性阈值阻止异常批量删除（参考 immich-gphotos 的 10%/500 项，但阈值是否适用 Amazon 须先实证验证）。绝不能删除不属于当前服务、当前账户已确认映射的资源；不实现永久清空回收站。
48. 默认取消同步/移除账户仅停止任务和忘记可选择的本地映射，任何 Amazon 云端资产/相册不得因取消配置而被删除。此约束优先于代码实现便利性。

### H. 工程规范与交付

49. 保留用户已有项目一致的工程偏好：Go 的明确接口、尽量少的外部依赖、不维护重复状态源、Cobra CLI、纯 Go SQLite、单一可执行文件、便于 Coding Agent 理解和测试、避免为想象中的未来添加层级。
50. 文档按 Diátaxis 分为 Tutorial / How-to / Reference / Explanation，重要架构权衡写 ADR；根级 Agent 指令简短、渐进披露，只有一份 `AGENTS.md`，不要创建面向特定 Agent 的复制指令文件。
51. CI 基于官方 GitHub Actions，包含 fmt、go vet、go test、race、go mod tidy 检查、lint 和工作流安全/语法检查（同系列项目采用 jactionlint）；公开发布多架构 OCI 镜像并使用不可变版本 tag，避免只提供 `main` 可变 tag。可使用 GoReleaser 统一二进制/容器元数据，但不为发布管线引入不必要的中转层。
52. 新仓库尚未创建；建议开源许可证采用 **AGPL-3.0-only**，与 Rainforest 保持一致；依赖的 MIT SDK 可按其许可证使用，原样保留第三方致谢。此许可证是本次为无上下文执行补齐的**默认拟定项，不代表用户过去已单独确认**；若仓库创建时另有决定，以明确仓库许可证为准。

## Testing Decisions

### 测试哲学及最高测试缝（testing seam）

1. **首选只有一条高层行为测试缝**：从公开的同步用例入口（例如“执行一次扫描与队列处理”）驱动真正的同步编排 + 临时 SQLite + 有界本地文件系统，以两个小型边界适配器分别注入 **Fake Immich** 和 **Fake Amazon**。断言对外行为（Amazon 上传了哪些字节、节点数、相册成员、是否会删除、状态是否能恢复），不测试私有 helper 调用顺序。
2. SDK 既有的 `http.RoundTripper` / `httptest.Server` 是**其现成适配器缝**；仅用它对 Amazon Adapter 的协议/故障分类做有限合同测试，不要在新服务里重新测试 SDK 已经覆盖的全部 HTTP 路由和请求序列。
3. Immich 端以官方 OpenAPI 文档建立**合同校验**：验证依赖的 endpoints、请求权限与关键响应字段；参考 immich-gphotos 的定期合同测试。使用固定版本快照做稳定 CI，再周期性验证上游发布版本，避免一次上游变更让所有 PR 测试不确定性失败。
4. 测试先写失败/恢复场景后实现；所有单元和集成测试默认**离线、无真实 Cookie、无外部云账户、可重复运行**，需要真实账户的端到端试点为人工可选独立任务，不进 PR 强制 CI。
5. 不因 Mock 返回成功就声称非美区、相册写入或媒体格式已真实验证；维护清楚的 `verified` / `experimental` / `untested` 证据表。

### 必须覆盖的外部行为场景

6. 首次选择两个相册，含共享同一 Asset ID：只上传一次原件，正确保留两条相册映射；重启再扫不新增重复节点。
7. 首次上传 5GB 级模拟流（不实际占用多 GB 内存或磁盘，可用生成流、受限写入桩），验证处理峰值内存有界；有限暂存额度耗尽时明确失败而不写入半成品。
8. Immich 源资产的 SHA-1 与实际下载字节不匹配：拒绝标记成功；未把 SHA-1 当作 Amazon MD5。
9. Amazon 已有同内容但不同 Immich Asset ID：只有确认 MD5/大小/身份符合时跳过；同名不同 MD5 必须报冲突或按用户策略处理；没有远端 MD5 不判定成功。
10. 上传返回成功且 Node ID 已在远端，但 SQLite 写入前进程断电：重启标记 `unconfirmed`，先查远端，再记录节点；不重复 POST。
11. 上传阶段成功、相册关联失败：仅重试相册关联，上传次数不变。
12. 不同相册共享一个云端 Node，源端只从其中一个相册撤出：P1 最多移除该相册中的成员关系，不删除媒体，另一相册仍然有它。
13. 空相册、找不到相册、401/403、500、超时和分页重复：不推进错误的扫描游标，不传播删除，状态可恢复。
14. 容器接收 SIGTERM 并重启：队列和暂存文件清理符合规则，后台工作可以继续且不盲目重复上传。
15. 短暂 429 和 Retry-After：实施有界退避；认证 Cookie 过期：任务暂停等待操作，不频繁刷接口，不抹去映射。
16. 高优先级新增照片到达时，正在运行的大型历史 Backfill 不阻塞其排队与执行；Webhook 重复、乱序、伪造请求都不会造成额外上传。
17. 恢复一个较老的 Immich 数据库快照，导致资产/相册瞬间少了很多：P0/P1 不发送任何媒体删除；P2 即使显式启用也被批量阈值拦下。
18. 单个源相册重命名、同名两个相册、目标相册创建时 409 或请求超时：映射保持稳定、不会错误绑定到另一个用户/相册。
19. 恶意/畸形原始文件名、服务地址、任意路径、Webhook payload：不得路径穿越或 SSRF；远程源 URL 不可由 Webhook 任意覆盖；Cookie、Token 和媒体访问 URL 不得出现在日志中。
20. 配置错误、纯轮询模式、无法创建 Workflow、重启后版本升级/SQLite 迁移、单实例锁竞争：诊断有明确结果；已有配置与数据不应丢失。
21. P2 多账户交叉测试：账户 A 的相册、Cookie、游标、节点映射不能读取或写入账户 B；移除 A 不影响 B。
22. ARM64 和 AMD64 容器构建与启动 smoke test、非 root、持久目录权限、Docker healthcheck、无外部依赖的 `doctor` 帮助输出检查。

### 验收方式

- 自动化至少包含离线高层行为测试、数据库持久化/迁移测试、适配器错误分类测试、CLI 契约测试与模拟 HTTP 服务测试；不能只检查单个函数返回值。
- 可选择的真实集成验证使用非敏感测试账号与无个人隐私的测试图片，由用户主动提供凭据；每个区域/媒体格式/相册操作分别记录实际是否通过。没有真实账号时自动化的完成标准是“实现并通过隔离测试，待人工线上验证”，不能写“所有区域已证实”。
- 所有修改必须通过仓库约定的单一质量门，且 Docker 容器重启/重复对账/错误故障注入测试不产生新的未识别副本或远端误删。

## Out of Scope

1. 在 Rainforest 桌面 UI 中添加 Immich 页面、后台 daemon、MyGo 改造或安装 Docker 的入口。
2. 对 Rainforest 现有 SQLite、凭据存储或 `internal` 实现建立编译或运行时依赖；共享同一个用户数据卷。
3. 再造 Amazon Photos SDK、使用 Python 原版守护程序或直接调用 Python 子进程。
4. Google Photos 传输、Google 逆向 API、无限免费存储方案及 Google OAuth。
5. 将 Amazon Photos 作为 Immich 的数据源，Amazon → Immich 或任意真正双向同步。
6. 在目标云上删除用户未通过本服务创建或明确绑定的资产、永久清空回收站或全库“清理重复文件”。
7. P0/P1 的媒体删除传播；P2 的删除传播必须独立受控，不作为完成基础同步的阻塞条件。
8. Cloud Gallery、面部识别、AI 标签、地图搜索、照片编辑、相册社交共享、Family Vault、Echo Show 设备控制 UI。
9. 修改 Immich 源代码/数据库，依赖宿主 Immich 私有表结构或强制挂载 Immich 原始目录。
10. 分布式多副本 Worker、Kubernetes Operator、Redis、外部队列、PostgreSQL、云托管 SaaS。
11. 自动绕过 Amazon MFA/CAPTCHA/会话过期、抓取用户密码或承诺未经验证的地区 API 一定可用。
12. 完全模仿 immich-gphotos 的浏览器凭据向导或复杂 Dashboard；如需要，放入 P2，不能牺牲 P0 的可维护性。
13. 修改/重新设计 SDK 与 Rainforest 全项目的 CI、CLI、文档风格；只在真实通用缺陷时向 SDK 提独立、最小范围修复。

## Further Notes

### 已定的取舍与明确补齐的默认项

| 事项 | 决策 | 来源/性质 |
|---|---|---|
| 项目位置 | 独立 Go Docker 项目，不嵌入 Rainforest | 前序对话已决定 |
| Amazon 实现 | 直接复用 `amazon-photos/pkg/aphotos` | 前序对话已决定 |
| 参考来源 | vanessa 参考场景；immich-gphotos 参考可靠性 | 前序对话已决定 |
| 同步方向 | Immich → Amazon Photos，默认不删媒体 | 前序对话已决定 |
| 开发顺序 | P0 可靠同步 → P1 事件及相册 → P2 多账户/Web UI | 前序对话已决定 |
| 仓库名 | `thedavidweng/immich-amazon-sync` | 为可执行开工拟定，可创建时再改名 |
| 协议 | AGPL-3.0-only，保留 MIT SDK 致谢 | 为自足规格补齐的建议默认项 |
| 默认扫描间隔 | 15 分钟；事件仅 P1 可用 | 为落地补齐的默认参数 |
| 管理界面 | P0/P1 仅 CLI/诊断；P2 轻量可选 Web UI | 延续先可靠后 UI 的决策 |
| 起始选择 | 显式相册 ID allowlist，P1 全库为显式选项 | 为防误操作补齐的默认政策 |
| 发布范围 | 单实例，Linux amd64/arm64 Docker，纯 Go | 用户的 NAS 与部署目标 |

### 给没有上下文的 Coding Agent 的执行指令

1. **先检查现状，不重复造轮子**：拉取并阅读 `rainforest`、`amazon-photos`、`immich-gphotos`、`immich-amazon-sync` 的当前主分支与相关 ADR/Glossary；如接口、API 或版本有变化，以实测和更新的权威来源为准，并记录偏差。新仓库在本文完成时**尚未存在**，不要把代码直接提交进 Rainforest。
2. **遵循现有项目语言和工程约束**：新仓库的根级 `AGENTS.md` 只记录简明必要规则；`CONTEXT.md`、`GLOSSARY.md`、`docs/adr/` 说明领域和取舍；CLI 用 Cobra；质检与文档按同系列 Go 项目实践，避免无益抽象。
3. **先搭建最低安全闭环**：Fake Immich/Amazon + SQLite + 单次同步命令 + 可恢复状态，再添加真实 Immich API Adapter、现有 SDK Adapter、Docker 构建和配置。优先证明掉电不重复上传、同名不同内容不会跳过、源端失联不传播删除。
4. **P0 可以独立交付**：仅相册 ID allowlist、周期扫描、原件暂存/上传/MD5 校验、SQLite、错误恢复、诊断、Docker，默认只写 Amazon 根目录或用户明确选择且已验证的目的位置；暂不承诺真正的相册镜像。第一版不应因为未实现 Workflow、Web Dashboard 或多账户而阻塞上线。
5. **P1 完成服务愿景的同步体验**：在 P0 基础上加 Immich Workflow/Webhook（能力可用才注册）、定期 Reconciler、低优先级 Backfill、Amazon 相册映射、多相册共享节点的正确成员关系和明确的全库模式。相册关联失败时不重复上传媒体。
6. **P2 解决操作体验**：多账户隔离、受保护 Web Dashboard 和经风险审核的显式删除传播（可单独延期，不影响核心产品）；无头 CLI/容器模式始终保持第一等公民地位。
7. **不自行提高未经证实的功能覆盖**：非美区真实上传、Amazon 相册行为、Motion Photos 展示、部分较旧 Immich Workflow 接口均需标记验证状态；不以 Mock 成功冒充真实兼容性。
8. **文档随实现提交**：README 明确用途、与 Rainforest/SDK 的关系、非官方免责声明、Docker Compose Quickstart、最小 API 权限、Cookie 获取/更新、备份及状态恢复、危险操作默认关闭、排错、限制与致谢；提供 Diátaxis 结构中的入门、参考、操作与原理章节。
9. **按阶段提交可审核的小 PR**，每阶段单独验收；若发现需要改变用户明确作出的架构或安全决定，记录 ADR 与 Issue，不擅自扩大产品范围。
10. **Issue 跟踪**：`to-spec` 原文要求发布项目 Issue 并标注 `ready-for-agent`。由于拟建仓库此时不存在，本文先作为可交付的 Markdown 规格；**建库后先创建总规格 Issue，添加 `ready-for-agent`，随后按阶段拆分实现 tickets，并在每张 ticket 引用此主 Issue**。不要把本规格误发到 Rainforest 的追踪 Issue。

### 分阶段 Definition of Done（验收门槛）

**P0 — Reliable album-source backup**

- [ ] 新仓库可构建单一 Go 二进制与 Linux amd64/arm64 Docker 镜像，并能用 Docker Compose 持久运行。
- [ ] 无真实密钥的配置示例；`albums list` 能列出可访问的相册 ID 供用户配置；只读 `doctor` 能诊断凭据、地区、Immich 版本/访问、存储权限。
- [ ] 指定 Immich 相册 ID 能分页发现原始媒体，选照片/视频，原件流式暂存并 MD5 验证；不保留整份内存副本。
- [ ] 同一资产重复扫描、容器中断、上传提交不确定、HTTP 429/401、磁盘满等故障均有可证明的安全恢复机制。
- [ ] SQLite 的任务及游标在重启、更新镜像、相册改名后保持正确；没有 Cloud Node ID 的伪成功。
- [ ] **无任何媒体删除路径**；对源失联、空异常结果不会破坏云端已有内容。
- [ ] 高层 Fake 测试、模拟 HTTP、CLI、容器与 CI 质量门通过；README/文档完整。

**P1 — Eventual consistency and album mirror**

- [ ] Immich Workflow/Webhook 在支持版本上加快新资产同步；不支持时轮询降级，事件可重放/去重。
- [ ] 每次 Reconcile/Backfill 有成功边界、游标持久化与崩溃恢复；新资产优先于大规模历史库存。
- [ ] Amazon 相册按 Immich 稳定 Album ID 映射，新增成员/移除成员与媒体本体分别处理。
- [ ] 重名、改名、共享资产、部分相册失败、权限和网络错误不会丢失关联或重传原件。
- [ ] 用户可显式启用资料库全量模式，默认相册范围不会升级后无故改变。
- [ ] Immich OpenAPI 合同验证、版本探测与非美区实际验证状态有文档记录。

**P2 — Operability and optional advanced policies**

- [ ] 单容器内多账号互不串数据，失败互不阻塞。
- [ ] 轻量且受认证保护的 Dashboard 能查看配置、进度、错误、暂停/继续/诊断，仍可完全不用 UI。
- [ ] 若实现删除传播，必须明确 Opt-in + dry-run + 类型化确认 + 不可绕过的批量阈值 + 完整审计测试；可以另开 Issue 延后。

### 参考资料（开工前必须检查主分支的新变化）

**既有仓库与接口**

- Rainforest：https://github.com/thedavidweng/rainforest
- Rainforest 产品边界：https://github.com/thedavidweng/rainforest/blob/main/docs/explanation/product-scope.md
- Rainforest 备份/对账 ADR：https://github.com/thedavidweng/rainforest/blob/main/docs/adr/0001-rainforest-architecture.md
- Rainforest 相册与目录/关联 ADR：https://github.com/thedavidweng/rainforest/blob/main/docs/adr/0006-mirrored-folder-structure.md
- Rainforest 领域术语：https://github.com/thedavidweng/rainforest/blob/main/GLOSSARY.md
- aphotos Go SDK：https://github.com/thedavidweng/amazon-photos/tree/main/pkg/aphotos
- aphotos SDK 参考：https://github.com/thedavidweng/amazon-photos/blob/main/docs/reference/sdk-api.md
- aphotos 冲突与提交验证：https://github.com/thedavidweng/amazon-photos/blob/main/docs/explanation/transfer-integrity-and-conflicts.md
- aphotos 区域与证据级别：https://github.com/thedavidweng/amazon-photos/blob/main/docs/reference/regions.md

**参考项目、官方 API**

- immich-gphotos：https://github.com/ImAleeexx/immich-gphotos
- immich-gphotos Reconciler：https://github.com/ImAleeexx/immich-gphotos/blob/main/src/immich_gphotos/sync/reconciler.py
- immich-gphotos Backfill：https://github.com/ImAleeexx/immich-gphotos/blob/main/src/immich_gphotos/sync/backfill.py
- immich-gphotos 相册映射：https://github.com/ImAleeexx/immich-gphotos/blob/main/src/immich_gphotos/sync/albums.py
- immich-amazon-sync：https://github.com/vanessa/immich-amazon-sync
- vanessa 源码：https://github.com/vanessa/immich-amazon-sync/blob/main/src/immich_sync.py
- Immich 官方 OpenAPI 指南：https://immich.app/docs/developer/open-api
- Immich 官方 OpenAPI JSON（实施时检查与实际 Immich 版本的一致性）：https://github.com/immich-app/immich/blob/main/open-api/immich-openapi-specs.json
- to-spec 技能原文：https://raw.githubusercontent.com/mattpocock/skills/refs/heads/main/skills/engineering/to-spec/SKILL.md

### 风险与假设登记

- **Amazon Photos 无公开正式 API**：第三方依赖的 v1 内部接口可能在任何时间失效；Cookie 也会过期。产品必须正面说明不提供云端 SLA。
- **区域覆盖不等于区域实证**：SDK 的静态路由表覆盖多个区域，但仅 US 有记录的真实验证；所有其它区域需要分层报告，不宣传“全地区稳定可用”。
- **Immich API 改变**：检查时官方主分支 OpenAPI metadata 版本为 `3.3.0-rc.0`，这是开发主分支的版本信息，**不是保证用户部署最新稳定版号**。必须按部署版本与 capability probe 处理兼容性，不把它硬编码为最低版本。
- **原件去重成本**：Immich 暴露的 `checksum` 为 base64 编码 SHA-1，而 Amazon SDK 的可用去重筛选为 MD5。首次上传前要取得原件（除非存在可信的本地 MD5 映射）；不能承诺与 Google 项目一样零下载回填。
- **相册语义**：Amazon 的 Folder Node 与 Visual Collection Album 是不同概念；Rainforest 镜像文件夹并不等于 Immich Album → Amazon Album 映射。P1 采用真正 Album Node 及成员关系，不能用同名文件夹假冒。
- **目标用途**：Echo Show 展示依赖设备自己的相册选择/刷新行为；服务只保证媒体与 Amazon Photos 相册关系，不保证设备何时刷新屏保。
- **暂无 Issue 目标仓库**：规格独立成文，没有制造已经发 Issue 或已经部署的假象。

---

**最终架构不变量**：`Immich (read only) → 独立 Go 同步服务 (durable SQLite + reconciliation) → aphotos Go SDK → Amazon Photos`。Rainforest 保持独立桌面应用；默认从不删除 Amazon 原始媒体；任何不确定上传状态必须对账而不是盲目重复提交。
