// Package syncstatus 提供一个进程内的"每桥接每循环最近一次同步结果"注册表。
//
// 为什么单独成包：
//
//	sync 引擎（写入方）与 web 层（读取方）都要访问它。若把类型放在
//	internal/sync 里，web 就得 import sync；而 sync 未来若要读 web 的东西
//	会形成环。抽成独立叶子包让两侧都只依赖它，无环。
//
// 为什么只放内存、不落库：
//
//	同步状态是纯运行时可观测数据——进程重启后一个同步周期（默认 60s）内
//	就会被四个循环重新填满。持久化它只会给每周期增加写库 IO，收益为负。
//	这与前端 useStatus 不持久化、每次拉最新的设计取向一致。
//
// 并发模型：
//
//	Record 由多个 bridgeWorker goroutine 并发调用；SnapshotFor 由 web
//	handler goroutine 调用。用一把 RWMutex 保护即可——写入频率极低
//	（每循环每周期一次），读取也只在运维打开面板时发生。
package syncstatus

import (
	"sort"
	"sync"
	"time"
)

// LoopStatus 是某个 bridge 的某条同步循环最近一次执行结果。
//
// ErrMsg 仅在 OK=false 时有意义；OK=true 时为空串。ElapsedMs 是该次
// 同步耗时（毫秒），供面板展示"同步快慢"。At 是记录时刻（UTC）。
type LoopStatus struct {
	Loop      string    `json:"loop"`
	OK        bool      `json:"ok"`
	ErrMsg    string    `json:"err_msg,omitempty"`
	ElapsedMs int64     `json:"elapsed_ms"`
	At        time.Time `json:"at"`
}

// Registry 是线程安全的"bridge → loop → 最近状态"两级表。
type Registry struct {
	mu sync.RWMutex
	m  map[string]map[string]LoopStatus
}

// New 构造一个空 Registry。进程内单例，由 main 创建后注入 supervisor 与 web。
func New() *Registry {
	return &Registry{m: make(map[string]map[string]LoopStatus)}
}

// Record 写入/覆盖某 bridge 某 loop 的最近一次同步结果。
//
// errMsg 只在 ok=false 时应传入；ok=true 时调用方传空串即可。本方法不
// 对 errMsg 做长度截断——上游 runStep 已经把错误规范化，且注册表只保留
// 每 (bridge,loop) 一条，总量恒定。
func (r *Registry) Record(bridge, loop string, ok bool, errMsg string, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inner, exists := r.m[bridge]
	if !exists {
		inner = make(map[string]LoopStatus)
		r.m[bridge] = inner
	}
	inner[loop] = LoopStatus{
		Loop:      loop,
		OK:        ok,
		ErrMsg:    errMsg,
		ElapsedMs: elapsed.Milliseconds(),
		At:        time.Now().UTC(),
	}
}

// SnapshotFor 返回给定 bridge 列表的状态快照。
//
// 只返回 bridges 参数里列出的桥接——调用方（web handler）传入当前 store
// 里存在的 bridge 名，天然把"已删除桥接"的残留状态过滤掉，无需单独的
// Prune 机制。每个 bridge 的 loop 列表按 loop 名排序，保证前端渲染稳定。
//
// 返回的 map 永远非 nil；某 bridge 尚无任何记录时不出现在结果里（前端
// 据此显示"等待首次同步"）。
func (r *Registry) SnapshotFor(bridges []string) map[string][]LoopStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]LoopStatus, len(bridges))
	for _, name := range bridges {
		inner, exists := r.m[name]
		if !exists || len(inner) == 0 {
			continue
		}
		loops := make([]LoopStatus, 0, len(inner))
		for _, ls := range inner {
			loops = append(loops, ls)
		}
		sort.Slice(loops, func(i, j int) bool { return loops[i].Loop < loops[j].Loop })
		out[name] = loops
	}
	return out
}
