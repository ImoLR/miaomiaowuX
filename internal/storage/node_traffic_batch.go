package storage

import (
	"context"
	"errors"
	"fmt"
)

// NodeTrafficItem 是 UpsertNodeTrafficBatch 的单条输入:某个 (tag,type) 当前的 cumulative 计数。
type NodeTrafficItem struct {
	Tag      string
	Type     string
	Uplink   int64
	Downlink int64
}

// UpsertNodeTrafficBatch 在单个事务里批量 upsert 一批 node_traffic 行,替代逐 tag 调 UpsertNodeTraffic
// (每次一个自动提交事务)。语义与逐条版完全一致:首见写 baseline(累计从0起、last=raw);
// isXrayRestarted 时把 last 归档进 total、delta 用新 raw;非重启时 delta=max(0,new-last)、total不变。
// isXrayRestarted 对整批统一(collector 按 server 的 xray_boot_time 判定)。合并后每次上报 node
// 写事务数从 N(tag数) 降到 1,消除高并发下单 SQLite writer 的写锁争用。
func (r *TrafficRepository) UpsertNodeTrafficBatch(ctx context.Context, serverID int64, items []NodeTrafficItem, isXrayRestarted bool) error {
	if r == nil || r.db == nil {
		return errors.New("traffic repository not initialized")
	}
	if serverID <= 0 {
		return errors.New("server id is required")
	}
	if len(items) == 0 {
		return nil
	}

	// 用 BEGIN IMMEDIATE 而不是默认 BeginTx(deferred):deferred 以读快照开始,首个 SELECT 取快照后
	// 再 INSERT/UPDATE 升级写锁 —— 期间别的连接提交了写就报 SQLITE_BUSY_SNAPSHOT(517),而 busy_timeout
	// **对 517 无效**,于是采集热路径持续刷 "database is locked"。IMMEDIATE 在事务开头就拿写锁:
	// 根除"读快照后升级",写锁被占时按 busy_timeout(5s)等待而非立刻失败。与 UpsertTrafficBatch 同款修法。
	// 手动 BEGIN/COMMIT 必须钉在同一条 conn 上(事务是连接级的),故用 db.Conn 取独占连接。
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for node traffic batch: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate node traffic batch: %w", err)
	}
	committed := false
	// 未提交则回滚,保证 conn 归还连接池前是干净状态。用 Background:即便 ctx 已取消,ROLLBACK 也要跑完。
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	rows, err := conn.QueryContext(ctx,
		`SELECT id, tag, type, total_uplink, total_downlink, last_uplink, last_downlink FROM node_traffic WHERE server_id = ?`,
		serverID)
	if err != nil {
		return fmt.Errorf("query node traffic batch: %w", err)
	}
	type existRow struct {
		id                                   int64
		totalUp, totalDown, lastUp, lastDown int64
	}
	existing := make(map[string]existRow)
	for rows.Next() {
		var e existRow
		var tag, typ string
		if err := rows.Scan(&e.id, &tag, &typ, &e.totalUp, &e.totalDown, &e.lastUp, &e.lastDown); err != nil {
			rows.Close()
			return fmt.Errorf("scan node traffic batch: %w", err)
		}
		existing[tag+"\x00"+typ] = e
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate node traffic batch: %w", err)
	}
	rows.Close()

	const insertStmt = `INSERT INTO node_traffic (server_id, tag, type, uplink, downlink, total_uplink, total_downlink, last_uplink, last_downlink, updated_at) VALUES (?, ?, ?, 0, 0, 0, 0, ?, ?, CURRENT_TIMESTAMP)`
	const updateStmt = `UPDATE node_traffic SET uplink = uplink + ?, downlink = downlink + ?, total_uplink = ?, total_downlink = ?, last_uplink = ?, last_downlink = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`

	for _, it := range items {
		if it.Tag == "" || (it.Type != "inbound" && it.Type != "outbound") {
			continue
		}
		e, ok := existing[it.Tag+"\x00"+it.Type]
		if !ok {
			if _, err := conn.ExecContext(ctx, insertStmt, serverID, it.Tag, it.Type, it.Uplink, it.Downlink); err != nil {
				return fmt.Errorf("insert node traffic %s/%s: %w", it.Tag, it.Type, err)
			}
			continue
		}
		var deltaUp, deltaDown, newTotalUp, newTotalDown int64
		if isXrayRestarted {
			newTotalUp = e.totalUp + e.lastUp
			newTotalDown = e.totalDown + e.lastDown
			deltaUp = it.Uplink
			deltaDown = it.Downlink
		} else {
			if it.Uplink >= e.lastUp {
				deltaUp = it.Uplink - e.lastUp
			}
			if it.Downlink >= e.lastDown {
				deltaDown = it.Downlink - e.lastDown
			}
			newTotalUp = e.totalUp
			newTotalDown = e.totalDown
		}
		if _, err := conn.ExecContext(ctx, updateStmt, deltaUp, deltaDown, newTotalUp, newTotalDown, it.Uplink, it.Downlink, e.id); err != nil {
			return fmt.Errorf("update node traffic %s/%s: %w", it.Tag, it.Type, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit node traffic batch: %w", err)
	}
	committed = true
	return nil
}
