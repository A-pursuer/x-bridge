# XBoard / XBridge 双端流量幂等修复（2026-11-23）

本轮已部署并回填源码，未提交远端、发布 dist、镜像或 Release。XBoard 基础仍为正式 V6.4.1 / `sha256:9a413f7fe9051613dd23ec84c9ba8681aafb3a3eb6b4998e258d970322863b21`，必须保留当前文件覆盖层。桥接运行 `v0.11.1-idempotency-20261123`；最终摘要见机器台账。

## 已确认决策

- 限速后置，等待 3x-ui 提供可用逐客户端 Mbps 接口后再评估，不自建流量整形。现存 speed_limit 权益字段保留，不代表远端执行限速。
- 保留逐节点 IP 限制，跨节点统一 IP 上限后置，本轮不加节点组件或集群改造。
- 同步数据库事务同时保证订阅扣量、默认账户投影、用户统计、节点统计及节点累计量。延迟批次按首次成功落库时的倍率、上海统计日期、当前用量周期计入；重放返回原结果。
- 先部署验证再回填源码；远端提交、正式发行需另获用户批准。

## 协议与数据

全部沿用 server.v2 鉴权。新增 `GET /api/v2/server/traffic/capabilities`、`POST /api/v2/server/traffic/batch`、`GET /api/v2/server/traffic/batch`。成功响应沿用 `{status,message,data,error}` 包装，data.state=processed 表示数据库事务已提交。

POST JSON 为 `{source_id,seq,batch_id,entries:[{subscription_id,u,d}]}`。source_id 为持久 32 位小写十六进制随机值，seq 从1递增，batch_id=`source_id-seq`。每批1～1000条、最多256KiB，拒绝字符串/布尔/负数/小数、重复订阅、越界或不属节点分组的订阅，不静默过滤。订阅即节点计费 UID。禁用节点、封禁或暂停/过期订阅仍可合法结算末次字节；不存在的订阅整批拒绝，需人工处理。

规范化 SHA256 输入：`source_id\nnode_id\nseq\nbatch_id\n` 后按 subscription_id 递增追加 `id:u:d\n`。GET 查询source_id/seq/batch_id；返回原processed或该source下一合法序号的missing。节点/source归属、序号或内容冲突返回409。桥接查询成功且missing才POST原批次，重启、超时均不新建替代批次；422亦锁定为conflict供核定。新桥接绝不退回 `/push`；旧客户端原push保持兼容。

XBoard新增 `v2_traffic_source` 和 `v2_traffic_batch`，永久保留去重凭据；不参与普通日志清理，禁止直接回滚迁移删除账本。`v2_stat_user.server_rate`扩大为decimal(20,8)以保留小数倍率分组，计费倍率冻结到8位小数；每订阅每方向计费字节向下取整，扣量和用户统计复用同一个结果，节点累计与统计用原始字节。节点行、账户、订阅加锁和唯一约束保护并发；出错整个事务回滚，不派发三份独立队列扣量。Redis待检查集合/最后上报缓存不属于账务ACK，失败不会使已提交批次重复扣量。

桥接新增 `traffic_sources`、`traffic_positions`、`traffic_outbox`，SQLite使用WAL+synchronous=FULL。事务先冻结payload与观察位置、占用序号，再发送；验证批次identity/hash/raw totals/processed_at，单事务推进全批确认位置和processed。观察累计与已确认位置分离：等待旧ACK期间持续采集，计数重置只追加新epoch字节，旧ACK只确认冻结位置，保留之后新流量。首次迁入仅承接旧基线未报差量，不重置历史累计。source绑定节点/面板/inbound，开始计费后修改物理映射需核定；名称未增加改名功能。

普通流量和撤权末次结算共用协议；旧unknown/sending不自动转新ID补报。新收尾batch_pending在processed回执后才移除客户端/基线。管理页显示最近10批状态、批次、订阅ID、说明和时间；全部账本在数据库中保留。

## 上线证据

完整上线前204归档：`/lzy/backup/xboard-xbridge/xboard-xbridge-backup-20261123-203110.tar.gz`，SHA256 `811bfc10a3bb1b92ffb01c1dd799c4a8b9a6a42fbfa0439abc2b152619e43114`。

证据目录：`/lzy/backups/xboard-idempotency/20261123T204000`。先迁移/部署XBoard，再短停桥接；旧协议近期22次push均有成功基线记录、无不确定在途。保留32条客户基线后切换桥接。Hysteria2停用成果保持，不重新连通验证。

- PHP节点相关14 tests / 92 assertions通过，其中新幂等4 tests覆盖扣量/投影/统计、事务故障回滚、非默认/封禁/禁用末次结算、倍率/跨日重放及422/409。
- Go全包通过；新增丢回执后重启查询、计数重置/新流量保留、确认故障全回滚和旧基线增量迁移测试。Vue类型/构建通过。
- 实际MySQL HTTP：同批12并发只生效一次；两批次订阅、账户、用户统计、节点统计/累计一致；冲突与跳号409；真实User流量API读取成功。
- 本机VLESS临时订阅完成真实代理请求；停用后1901上行/5458下行字节通过新协议结算、processed后移除客户端。数据库完整性和双端20批回执逐项匹配；这是观测时刻数量，后续正常增长。
- 实际桥接页显示processed/UID/时间，pageerror=0。专用账户/订阅/套餐/分组/节点、浏览器会话与远端客户端均已精确清理；生产source顺序和已提交回执保留。

## 安装、备份、恢复与回退

203继续固定本机accepted包、安装器和摘要，不调用latest。204仍保留3天，整个bridge.db包含新账本，匹配二进制与安装器同归档。源码/本机216恢复器增加协议检查：新桥接库若缺配套XBoard接收端或账本则拒绝启动并保持维护。正式216应用坐标仍为6.4.1；本次没有生成/发行新的216镜像坐标，也没有执行203重装或216恢复演练。

当前204在线备份的两个数据库不是分布式原子快照，恢复后可能有边界差异；必须保留两端账本并核对pending/source/seq。新账本归档不能直接配旧基础镜像开启服务，先应用本轮accepted-runtime-overlay并核对配套接口。单独回灌任一数据库、删除source/outbox、生成新source绕过冲突均不安全。需要回退时先停桥接、保留账本并对账；禁止用旧二进制盲重发。安装器在已有新账本时只接受支持traffic-batch-v1的本地验收包。

范围限制：不补历史错误扣量，不重建历史倍率/周期；无法恢复3x-ui在两次观察之间被外部清零且从未读到的字节；整套双端账本丢失不等于可自动幂等恢复。未对每台远端制造网络中断或重置真实客户计数。

## 本轮最终归档坐标

- 桥接二进制 SHA256：`cb5f2350e54266bdccd8850ba4efc4860c7729001eb2a55075f20eed58f98792`。
- 后置204归档：`/lzy/backup/xboard-xbridge/xboard-xbridge-backup-20261123-205245.tar.gz`，SHA256 `2f403475d968116fb9aa5bf71fd515c2d1c6e8a2b76b5f6eae4827423a713814`；86 文件摘要/SQLite完整性/配套二进制通过。
- 完整文件覆盖层：`/lzy/backups/xboard-idempotency/20261123T204000/accepted-runtime-overlay.tar.gz`，SHA256 `2d8333906bb86b0a010d82d3ec1cb2cfdb04a397231dca0fccd85fca79fa1165`。保留这层与匹配账本，正式基础镜像未包含本轮修复。
