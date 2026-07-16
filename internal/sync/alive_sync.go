package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	stdsync "sync"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/protocol"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/store"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xboard"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xui"
)

// syncAlive 把 3x-ui 端"在线 client + 其使用的 IP"上报到 Xboard /report(alive)。
//
// 算法：
//
//  1. 拉所有当前 inbound 的 client 流量列表（GetClientTrafficsByInboundID）取
//     该 inbound 内的 email 集合 myEmails。
//  2. 拉全局在线 email（GetOnlines）取 onlineAll。
//  3. onlineMine = onlineAll ∩ myEmails；逐个调 GetClientIPs 解析最近 IP。
//  4. 把 (xboard_user_id → [ip...]) 形态写入 AliveMap，调 /report 上报 alive 字段。
//
// 并发：GetClientIPs 是逐个 email 的串行接口，inbound 内在线用户多时延迟会显著。
//
//	采用受限并发（信号量限并发数 8）平衡 3x-ui 面板压力 vs 吞吐。
//
// 错误处理（v0.6 单一正向路径承诺）：
//
//	a) 某个 email 拉 IPs 失败：返回错误中断本次循环；下个周期完整重做（在
//	   线 IP 是状态而非增量，重做幂等且不丢数据）。v0.5.x 时代的"逐个跳过
//	   失败 email"已被移除——失败信号必须显式可见，而不是被静默吞掉。
//	b) /report(alive) 失败：返回错误中断本次循环；语义同上。
//
// v0.6 关键变更：移除 placeholder IP 兜底
//
//	v0.5.2 为了让 3x-ui 端 inbound_client_ips 表常常空（运维未启用 LimitIP /
//	access log / Fail2Ban 时的常态）的部署仍能在 Xboard 后台看到非 0 的
//	"在线设备"显示，构造了 RFC 5737 TEST-NET-2 段的 placeholder IP（从
//	email fnv32a hash 推导稳定占位）。这是典型的"失败兜底" / "降级路径"——
//	它让运维看到的"在线设备 1"与"3x-ui 端 LimitIP 已配置 + access log
//	可读"两件事产生伪相关，掩盖真正的根因（多数情况下根因是运维未按 3x-ui
//	文档启用 LimitIP/access log）。
//
//	v0.6 起严格遵守"单一正向路径"承诺：GetClientIPs 返回空切片时（即 3x-ui
//	端确实无该 email 的 IP 记录），该 user 不进入 alive 上报。Xboard 端
//	自然显示"在线设备=0"——这是数据真相而非伪展示。运维若需真实 IP，应按
//	3x-ui 官方文档启用 LimitIP + access log（Linux 还需 Fail2Ban）；不再由
//	中间件构造伪 IP 欺骗 Xboard 端 setDevices。
//
//	已废弃函数：placeholderIP / mergeUnique（v0.5.2 引入用于 placeholder
//	路径）。本文件不再保留它们以避免误导后续维护者认为可以"继续兜底"。
func (w *bridgeWorker) syncAlive(ctx context.Context) error {
	// 取本次 tick 的 trace 化 logger（含 loop=alive_sync + trace_id）；
	// 详见 engine.go runStep 中的 trace_id 注入逻辑。下方 fan-out goroutine
	// 闭包捕获 log 引用，所有"alive 拉 IP 失败" 与"alive 上报完成"INFO
	// 共享同一组 attrs。
	log := loggerFromCtx(ctx, w.log)

	traffics, err := w.xuiC.GetClientTrafficsByInboundID(ctx, w.cfg.XuiInboundID)
	if err != nil {
		return fmt.Errorf("拉取 inbound 流量列表（用于 alive 过滤）：%w", err)
	}
	myEmails := make(map[string]struct{}, len(traffics))
	for _, t := range traffics {
		if protocol.IsManaged(t.Email) {
			myEmails[t.Email] = struct{}{}
		}
	}

	online, err := w.xuiC.GetOnlines(ctx)
	if err != nil {
		return fmt.Errorf("拉取在线 email：%w", err)
	}

	// 求交集，并把 email 翻译成 (xboard_user_id, uuid)；过滤掉 baseline 缺失的。
	var targets []liveTarget
	for _, email := range online {
		if _, ok := myEmails[email]; !ok {
			continue
		}
		bl, err := w.store.GetBaseline(ctx, w.cfg.Name, email)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("读取 alive 基线 %s：%w", email, err)
		}
		targets = append(targets, liveTarget{email: email, xboardUserID: bl.XboardUserID})
	}

	results, source, err := w.collectAliveIPs(ctx, targets, log)
	if ctx.Err() != nil {
		// 外部已取消，不再上报。
		return ctx.Err()
	}
	if err != nil {
		return err
	}

	// 拼装 AliveMap：key 是 user_id 字符串。
	//
	// 跳过条件（v0.6 单一正向路径）：
	//   - userID == 0：baseline 异常，绝不可能（targets 已经按 baseline
	//     存在筛选过）；防御性跳过仅作 trip-wire；
	//   - len(ips) == 0：3x-ui 端确实无该 email 的 IP 记录；该 user 不进
	//     入本次 alive 上报，Xboard 端将自然显示"该 user 离线"。这不是
	//     失败兜底——是"无数据则不上报"的客观行为。
	alive := xboard.AliveMap{}
	for _, r := range results {
		if r.userID == 0 || len(r.ips) == 0 {
			continue
		}
		key := strconv.FormatInt(r.userID, 10)
		alive[key] = mergeUnique(alive[key], r.ips)
	}

	if err := w.xboardC.PushReport(ctx, w.cfg.XboardNodeID, w.cfg.XboardNodeType, xboard.Report{Alive: alive}); err != nil {
		return fmt.Errorf("Xboard /report(alive)：%w", err)
	}
	log.Info("alive 上报完成", "user_count", len(alive), "alive_ip_source", source)
	return nil
}

type liveTarget struct {
	email        string
	xboardUserID int64
}

type ipResult struct {
	userID int64
	ips    []string
}

func (w *bridgeWorker) collectAliveIPs(ctx context.Context, targets []liveTarget, log *slog.Logger) ([]ipResult, string, error) {
	results := make([]ipResult, len(targets))
	if len(targets) == 0 {
		return results, "none", nil
	}

	panelGuid, guidErr := w.xuiC.GetPanelGuid(ctx)
	if guidErr != nil {
		log.Debug("alive 批量 IP：panelGuid 获取失败，仍尝试批量树并允许回退", "err", guidErr)
	}
	tree, err := w.xuiC.GetClientIPsByGuid(ctx)
	if err != nil {
		log.Warn("alive 批量 IP 拉取失败，回退逐 email 拉取", "err", err)
		legacy, legacyErr := w.collectAliveIPsLegacy(ctx, targets)
		return legacy, "fallback", legacyErr
	}

	var missing []liveTarget
	for i, t := range targets {
		ips := extractBatchIPs(tree, t.email)
		if len(ips) == 0 {
			missing = append(missing, t)
			continue
		}
		results[i] = ipResult{userID: t.xboardUserID, ips: ips}
	}
	if len(missing) == 0 {
		log.Debug("alive 批量 IP 命中", "panel_guid", panelGuid, "target_count", len(targets))
		return results, "batch", nil
	}

	legacy, legacyErr := w.collectAliveIPsLegacy(ctx, missing)
	if legacyErr != nil {
		return nil, "batch+fallback", legacyErr
	}
	legacyIdx := 0
	for i := range targets {
		if len(results[i].ips) != 0 {
			continue
		}
		results[i] = legacy[legacyIdx]
		legacyIdx++
	}
	log.Debug("alive 批量 IP 部分命中，缺失项回退逐 email 拉取",
		"panel_guid", panelGuid,
		"target_count", len(targets),
		"fallback_count", len(missing),
	)
	return results, "batch+fallback", nil
}

func (w *bridgeWorker) collectAliveIPsLegacy(ctx context.Context, targets []liveTarget) ([]ipResult, error) {
	const maxConcurrency = 8
	sem := make(chan struct{}, maxConcurrency)
	results := make([]ipResult, len(targets))

	var (
		wg       stdsync.WaitGroup
		errOnce  stdsync.Once
		firstErr error
	)
	for i := range targets {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			t := targets[idx]
			raw, err := w.xuiC.GetClientIPs(ctx, t.email)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("alive 拉 IP %s：%w", t.email, err)
				})
				return
			}
			results[idx] = ipResult{userID: t.xboardUserID, ips: extractIPs(raw)}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func extractBatchIPs(tree map[string]map[string][]xui.ClientIpEntry, email string) []string {
	type observed struct {
		ip string
		ts int64
	}
	latest := map[string]int64{}
	for _, perEmail := range tree {
		for _, entry := range perEmail[email] {
			if entry.IP == "" {
				continue
			}
			if cur, ok := latest[entry.IP]; !ok || entry.Timestamp > cur {
				latest[entry.IP] = entry.Timestamp
			}
		}
	}
	entries := make([]observed, 0, len(latest))
	for ip, ts := range latest {
		entries = append(entries, observed{ip: ip, ts: ts})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ts == entries[j].ts {
			return entries[i].ip < entries[j].ip
		}
		return entries[i].ts > entries[j].ts
	})
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ip)
	}
	return out
}

// extractIPs 把 3x-ui clientIps 的字符串数组解析为纯 IP 列表。
//
// 3x-ui 返回形如 ["1.2.3.4 (2024-01-02 15:04:05)", "5.6.7.8 (...)"]
// 本函数取每行首个空白前的 token 作为 IP；不做合法性校验——非法 IP 由
// Xboard 端处理（DeviceStateService 会过滤），中间件不重复校验。
func extractIPs(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 取首个 token：3x-ui 间隔可能用普通空格，也可能用其它空白；strings.Fields 兼容。
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

// mergeUnique 把 b 中的元素合并到 a 中，去重；保留 a 原有顺序，b 中新元素追加在末尾。
//
// 不使用 map 是为了保证出现顺序确定（便于日志比对），且 IP 列表通常 < 16 个，O(n^2) 可接受。
//
// 当前调用点（alive 拼装）：在多个 IP 来源合并到同一 user_id 时去重——
// 例如 3x-ui 返回的同 IP 多次（含不同时间戳）会被合并为一条。本函数与
// v0.5.2 时代的 placeholder 合并语义无关（placeholder 路径已在 v0.6 删除）；
// 保留只为正常 IP 列表的合并需求。
func mergeUnique(a, b []string) []string {
	for _, v := range b {
		seen := false
		for _, x := range a {
			if x == v {
				seen = true
				break
			}
		}
		if !seen {
			a = append(a, v)
		}
	}
	return a
}
