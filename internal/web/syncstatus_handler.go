package web

import (
	"net/http"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/syncstatus"
)

// syncstatus_handler.go 暴露 GET /api/sync-status（fork 可观测性扩展）。
//
// 返回"当前存在的每个桥接 → 各同步循环最近一次结果"，供前端在桥接卡片上
// 展示"最近同步"聚合灯。数据来自进程内内存注册表（syncstatus.Registry），
// 不落库；进程重启后一个同步周期内自动重新填充。

// syncStatusResponse 是 GET /api/sync-status 的返回结构。
//
// bridges 以桥接名为 key，value 是该桥接各循环的最近状态列表（按 loop
// 名排序）。尚无任何同步记录的桥接不出现在 map 里——前端据此显示
// "等待首次同步"。
type syncStatusResponse struct {
	Bridges map[string][]syncstatus.LoopStatus `json:"bridges"`
}

// handleGetSyncStatus 处理 GET /api/sync-status。
//
// 先读 store 里当前的桥接名列表，再用它向注册表取快照——这样已删除桥接
// 的残留状态被天然过滤掉（SnapshotFor 只返回传入名字里存在记录的项）。
func (s *Server) handleGetSyncStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListBridges(r.Context())
	if err != nil {
		s.log.Error("sync-status: ListBridges 失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, errCodeInternal, "查询桥接列表失败")
		return
	}
	names := make([]string, 0, len(rows))
	for i := range rows {
		names = append(names, rows[i].Name)
	}
	s.writeJSON(w, http.StatusOK, syncStatusResponse{
		Bridges: s.syncReg.SnapshotFor(names),
	})
}
