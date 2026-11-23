# 2026-11-23 本地部署覆盖

起点：本地与远端main同为80e2a3006ff1ad697b64481a38e38e1fe809c386，运行二进制与正式v0.11.1逐字节一致。当前为已部署、已回填但未提交/发布的v0.11.1-retention-20261123；运行二进制SHA-256为fc6f653d3a98df3817ee941c39e25a12bc3825c01bc072e771abf22962b3afaf。

## 行为

- 用户拉取15秒，统计周期不变；UID原样使用XBoard订阅ID。面板写入后读回；不比较未显式管理的自动subId，避免重复更新。
- 用户移除与流量循环互斥。traffic_retirements保存停用、末次统计、差量、发送及清理状态；sending/unknown不盲目重发或硬删，明确拒绝可重试。accepted仅表示XBoard队列受理，不是落库ACK。常规流量上报未做双端幂等重构。
- API/UI显示收尾计数、UID及原因。含基线或未结清记录的桥接不能直接删除。名称继续作为内部键，不新增改名功能；迁移用户并完成结算后才可删除重建。
- 限速执行、跨节点统一IP上限未实现。3x-ui的limitIp是逐节点IP限制，不是物理设备授权。

## 运行与恢复

服务xboard-xui-bridge，二进制/usr/local/bin/xboard-xui-bridge，SQLite位于/usr/local/xboard-xui-bridge/data/bridge.db。验收包位于/usr/local/lib/xbridge-selfhost，203.sh固定本地二进制、版本及安装器摘要；未发布包缺失时拒绝降级为latest。install.sh在服务操作前验证本地输入，远端发行缺摘要时拒绝安装。

204.sh的3天在线备份纳入匹配二进制、架构/摘要、systemd、安装器及验收包。XBoard恢复脚本识别可选材料；新收尾schema没有匹配二进制时停止。没有执行203重装或216清场恢复。

备份和证据：/lzy/backups/xboard-retention/20261123T172605。Go全包测试、Vue构建通过；两条专用订阅真实下发、批量封禁、completed收尾及精确清理通过。不要重写未知账本；先核对XBoard实际处理记录，再决定是否补报。

## 节点策略与证据范围

实际3x-ui使用PostgreSQL和Xray在线连接统计；磁盘旧SQLite和access=none不能代表执行失效。8台节点改为旧IP封300秒，仅代理端口44333/44301，管理端口保留，访问日志保持none。超限记录0600、小时轮转清理。

日本VLESS无日志双独立来源限1/2/不限实测通过；8台均完成配置/运行检查。Hysteria2试用连接失败，未证明该协议的限制生效；日本目标无公开IPv6，不宣称IPv6端到端通过。其余节点不因同版本配置就自动获得相同执行验收结论。

节点回退材料在各机/root/xboard-retention-20261123T172605；不能把整库恢复当作配置回退。用户尚未授权正式发布，当前PAT不进入源码、脚本或日志。
