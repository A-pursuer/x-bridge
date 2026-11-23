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
type retirementDetail struct {
	UID         int64  `json:"uid"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
	UpdatedAt   int64  `json:"updated_at"`
	PendingUp   int64  `json:"pending_up"`
	PendingDown int64  `json:"pending_down"`
}
type batchDetail struct {
	BatchID   string  `json:"batch_id"`
	Seq       int64   `json:"seq"`
	State     string  `json:"state"`
	Detail    string  `json:"detail"`
	UpdatedAt int64   `json:"updated_at"`
	UIDs      []int64 `json:"subscription_ids"`
}
type syncStatusResponse struct {
	TrafficBatches map[string][]batchDetail `json:"traffic_batches"`

	RetirementDetails map[string][]retirementDetail      `json:"retirement_details"`
	Bridges           map[string][]syncstatus.LoopStatus `json:"bridges"`
	Retirements       map[string]map[string]int          `json:"retirements"`
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
	pending := map[string]map[string]int{}
	batches := map[string][]batchDetail{}
	details := map[string][]retirementDetail{}
	for _, name := range names {
		outbox, e := s.store.TrafficBatchStatus(r.Context(), name)
		if e != nil {
			s.writeError(w, 500, errCodeInternal, "查询流量批次失败")
			return
		}
		for _, b := range outbox {
			uids := []int64{}
			for _, entry := range b.Batch.Entries {
				uids = append(uids, entry.SubscriptionID)
			}
			batches[name] = append(batches[name], batchDetail{BatchID: b.Batch.BatchID, Seq: b.Batch.Seq, State: b.State, Detail: b.Detail, UpdatedAt: b.UpdatedAt, UIDs: uids})
		}
		items, e := s.store.ListRetirements(r.Context(), name)
		if e != nil {
			s.writeError(w, 500, errCodeInternal, "查询流量收尾失败")
			return
		}
		counts := map[string]int{}
		for _, item := range items {
			counts[item.State]++
			details[name] = append(details[name], retirementDetail{UID: item.UID, State: item.State, Detail: item.Detail, UpdatedAt: item.UpdatedAt, PendingUp: item.Up, PendingDown: item.Down})
		}
		pending[name] = counts
	}
	s.writeJSON(w, http.StatusOK, syncStatusResponse{
		TrafficBatches:    batches,
		Retirements:       pending,
		RetirementDetails: details,
		Bridges:           s.syncReg.SnapshotFor(names),
	})
}
