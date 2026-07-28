package handler

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 真探针数据后端的内存态。伪装探针页要像哪吒探针那样展示各服务器的 CPU/内存/硬盘 + ping 延迟曲线,
// 但用户选择「仅内存实时滚动」——不建 DB 时序表,主控内存维护环形缓冲,重启清空。
//
// 单例挂到 RemoteWSHandler(写)与 ProbePublicHandler(读),二者同包。仿 collector.go 的 serverSpeeds/speedMu。

// ProbePingTarget 是一个 ping 目标(省市×运营商),管理员从 CDN 列表勾选。host 形如
// "he-cu-v4.ip.zstaticcdn.com:80"(已含端口)或 host+port 分离。Key 全局唯一(如 "he-cu-v4")。
type ProbePingTarget struct {
	Key   string `json:"key"`
	Label string `json:"label"` // 展示名,如「河北联通」
	ISP   string `json:"isp"`   // unicom/mobile/telecom;国际目标用 intl
	Host  string `json:"host"`  // 目标主机(不含端口)
	Port  int    `json:"port"`
	// Type 是探测方式:"icmp" 或 "tcp"(空=tcp,兼容存量配置)。
	// **不进公开响应** —— 和 host/port 同级敏感(泄露探测方式)。
	Type string `json:"type,omitempty"`
}

// parseProbePingTargetOverrides 解析 per-server ping 目标覆盖(probeDisguisePingTargetsOverrideKey)。
// 返回 map 的**键存在性**即语义:存在(哪怕值为空切片)=该机用自己的列表;不存在=跟随全局。
// 解析失败返回 nil,调用方一律回落全局,不因一条坏 JSON 让所有服务器停止探测。
func parseProbePingTargetOverrides(raw string) map[int64][]ProbePingTarget {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var m map[string][]ProbePingTarget
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	out := make(map[int64][]ProbePingTarget, len(m))
	for k, v := range m {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			continue
		}
		if v == nil {
			v = []ProbePingTarget{}
		}
		out[id] = v
	}
	return out
}

// ProbeSysSnapshot 是一台服务器最新的系统指标(agent 上报的开启项;未开启的字段为 nil 指针语义靠上层处理)。
type ProbeSysSnapshot struct {
	CPUPct    float64 `json:"cpu_pct"`
	LoadAvg   string  `json:"loadavg"`
	MemUsed   int64   `json:"mem_used"`
	MemTotal  int64   `json:"mem_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	// 掩码:agent 只上报开启项,这里记录哪些字段有效(避免 0 值被当成真实数据)。
	HasCPU  bool  `json:"has_cpu"`
	HasMem  bool  `json:"has_mem"`
	HasDisk bool  `json:"has_disk"`
	At      int64 `json:"at"` // unix 秒
}

// ProbeLatencySample 是一次 ping 的结果(agent 上报)。
type ProbeLatencySample struct {
	Key       string `json:"key"`
	Success   bool   `json:"success"`
	LatencyMs int64  `json:"latency_ms"`
	// At 是 agent 侧的采样时刻(unix 秒)。老 agent 不发,为 0 时回落到接收时刻。
	At int64 `json:"at,omitempty"`
}

// probeLatencyPoint 是原始点 ring 里的一个延迟点。
type probeLatencyPoint struct {
	Ts        int64 `json:"t"`  // unix 秒
	LatencyMs int64 `json:"ms"` // -1 表示本次探测失败
}

// probeAggSlot 是一个 5 分钟聚合槽。
//
// 为什么要有聚合层:原始点 ring 的覆盖窗口 = 容量 × 上报间隔,想靠它撑起 24 小时
// 就得存上万个点(每目标每服务器),内存和 Snapshot 拷贝都受不了。聚合成 5 分钟槽后
// 288 槽正好 24 小时,每目标只要 ~9KB,而 5 分钟粒度对"看趋势"完全够用。
type probeAggSlot struct {
	Slot int64 // 槽起始时刻(unix 秒,已按 probeAggSlotSec 对齐)
	Sum  int64 // 成功点延迟之和
	Cnt  int64 // 成功点数
	Fail int64 // 失败点数
}

// AvgMs 返回该槽平均延迟;无成功点返回 -1。
func (a probeAggSlot) AvgMs() int64 {
	if a.Cnt <= 0 {
		return -1
	}
	return a.Sum / a.Cnt
}

// LossPct 返回该槽丢包率(0~100);无任何点返回 -1。
func (a probeAggSlot) LossPct() float64 {
	total := a.Cnt + a.Fail
	if total <= 0 {
		return -1
	}
	return float64(a.Fail) * 100 / float64(total)
}

const (
	probeAggSlotSec  = 300 // 聚合槽宽度:5 分钟
	probeAggMaxSlots = 288 // 288 × 5min = 24 小时
	probeRawCapN     = 60  // 原始点只用来算"当前延迟",不承担历史展示,不需要留很多
)

type probeServerMetrics struct {
	sys       ProbeSysSnapshot
	hasSys    bool
	latency   map[string][]probeLatencyPoint // targetKey -> 最近 capN 个原始点(算 current 用)
	agg       map[string][]probeAggSlot      // targetKey -> 最近 288 个 5 分钟槽(历史展示用)
	lastAt    map[string]int64               // targetKey -> 已 ingest 的最新采样时刻,用于去重
	updatedAt int64
}

// ProbeMetricsStore 内存 ring:每服务器最新系统指标 + 每目标原始点 + 5 分钟聚合槽。并发安全。
type ProbeMetricsStore struct {
	mu   sync.RWMutex
	data map[int64]*probeServerMetrics // serverID -> metrics
	capN int                           // 每目标原始点保留上界
}

// NewProbeMetricsStore capN 为每目标原始点环形容量(仅用于算当前延迟;
// 历史曲线走固定 288 槽的 5 分钟聚合层,与该参数无关)。
func NewProbeMetricsStore(capN int) *ProbeMetricsStore {
	if capN <= 0 {
		capN = probeRawCapN
	}
	return &ProbeMetricsStore{data: make(map[int64]*probeServerMetrics), capN: capN}
}

func (s *ProbeMetricsStore) ensure(serverID int64) *probeServerMetrics {
	m, ok := s.data[serverID]
	if !ok {
		m = &probeServerMetrics{
			latency: make(map[string][]probeLatencyPoint),
			agg:     make(map[string][]probeAggSlot),
			lastAt:  make(map[string]int64),
		}
		s.data[serverID] = m
	}
	return m
}

// IngestSys 写入某服务器最新系统指标。
func (s *ProbeMetricsStore) IngestSys(serverID int64, snap ProbeSysSnapshot) {
	now := time.Now().Unix()
	snap.At = now
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.ensure(serverID)
	m.sys = snap
	m.hasSys = true
	m.updatedAt = now
}

// IngestLatency 追加一批 ping 结果:写原始点 ring + 累进 5 分钟聚合槽。
//
// 时间轴用样本自带的采样时刻(smp.At),不是接收时刻 —— 上报搭的是 traffic tick(5s)的车,
// 与 ping 周期不同频,用接收时刻会把时间轴压扁。老 agent 不发 At,回落接收时刻。
// 同一采样时刻的重复上报直接丢弃(老 agent 没有去重,会把同一轮结果报很多次)。
func (s *ProbeMetricsStore) IngestLatency(serverID int64, samples []ProbeLatencySample) {
	if len(samples) == 0 {
		return
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.ensure(serverID)
	for _, smp := range samples {
		ts := smp.At
		if ts <= 0 {
			ts = now
		}
		// 未来时间戳(agent 时钟跑偏)会把聚合槽推到前面去,后续正常点全被判成过期。
		if ts > now+300 {
			ts = now
		}
		if last, ok := m.lastAt[smp.Key]; ok && ts <= last {
			continue // 这一轮已经收过了
		}
		m.lastAt[smp.Key] = ts

		ms := smp.LatencyMs
		if !smp.Success {
			ms = -1
		}

		pts := append(m.latency[smp.Key], probeLatencyPoint{Ts: ts, LatencyMs: ms})
		if len(pts) > s.capN {
			pts = pts[len(pts)-s.capN:]
		}
		m.latency[smp.Key] = pts

		slot := ts - ts%probeAggSlotSec
		slots := m.agg[smp.Key]
		if n := len(slots); n > 0 && slots[n-1].Slot == slot {
			if ms < 0 {
				slots[n-1].Fail++
			} else {
				slots[n-1].Sum += ms
				slots[n-1].Cnt++
			}
		} else {
			ns := probeAggSlot{Slot: slot}
			if ms < 0 {
				ns.Fail = 1
			} else {
				ns.Sum, ns.Cnt = ms, 1
			}
			slots = append(slots, ns)
			if len(slots) > probeAggMaxSlots {
				slots = slots[len(slots)-probeAggMaxSlots:]
			}
		}
		m.agg[smp.Key] = slots
	}
	m.updatedAt = now
}

// PruneKeys 删掉不在 keep 里的目标(管理员缩减目标列表后,ring 里的孤儿键)。
func (s *ProbeMetricsStore) PruneKeys(serverID int64, keep map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data[serverID]
	if !ok {
		return
	}
	for k := range m.latency {
		if !keep[k] {
			delete(m.latency, k)
			delete(m.agg, k)
			delete(m.lastAt, k)
		}
	}
}

// ProbeTargetSeries 是单个 ping 目标的快照:当前延迟 + 最近若干个 5 分钟聚合槽。
type ProbeTargetSeries struct {
	CurrentMs int64          // 最新一个原始点的延迟;-1=当前探测失败,无数据也为 -1
	Slots     []probeAggSlot // 按时间递增,最多 maxSlots 个
}

// ProbeServerView 是给公开端点/内部读取用的快照(值拷贝,读锁下产生,调用方无需再锁)。
type ProbeServerView struct {
	HasSys  bool
	Sys     ProbeSysSnapshot
	Latency map[string]ProbeTargetSeries
}

// Snapshot 返回某服务器的指标快照拷贝,每目标最多带 maxSlots 个最近的聚合槽。
//
// maxSlots 必须由调用方按实际要展示的窗口给:列表页只画近 1 小时(12 槽),
// 一律拷满 288 槽会让一次无鉴权的公开 GET 产生几 MB 的堆分配,而它是 5 秒一轮询的。
// maxSlots <= 0 表示全取(详细曲线端点用,但那是单服务器单目标,量很小)。
func (s *ProbeMetricsStore) Snapshot(serverID int64, maxSlots int) (*ProbeServerView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.data[serverID]
	if !ok {
		return nil, false
	}
	lat := make(map[string]ProbeTargetSeries, len(m.agg))
	for k, slots := range m.agg {
		if maxSlots > 0 && len(slots) > maxSlots {
			slots = slots[len(slots)-maxSlots:]
		}
		cp := make([]probeAggSlot, len(slots))
		copy(cp, slots)

		cur := int64(-1)
		if pts := m.latency[k]; len(pts) > 0 {
			cur = pts[len(pts)-1].LatencyMs
		}
		lat[k] = ProbeTargetSeries{CurrentMs: cur, Slots: cp}
	}
	return &ProbeServerView{HasSys: m.hasSys, Sys: m.sys, Latency: lat}, true
}

// Evict 清掉 updatedAt 早于 cutoff 的服务器(掉线服务器),防内存无界增长。
func (s *ProbeMetricsStore) Evict(olderThan time.Duration) {
	cutoff := time.Now().Add(-olderThan).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.data {
		if m.updatedAt < cutoff {
			delete(s.data, id)
		}
	}
}
