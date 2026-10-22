package loadbalancer

// paramstrip_store.go 定义「学到的参数裁剪规则」如何跨进程重启存活。
//
// 问题：paramStripLearned 是一个纯内存 map。网关在生产里一天重启 8 次
// （蓝绿交接），每次重启都把这批学到的规则清零，于是同一个渠道同一个模型
// 的同一个参数在每个新进程里都要先错一次、再学一次。生产实测 2026-10-21
// 凌晨：#84 在 03:07 学到、03:57 重新学到；#226 在 03:31 学到、04:11 重新
// 学到；#54 在 03:59 学到、04:27 重新学到。运维只能用 yaml 的 strip_params
// 手工补，补漏的那几个就无限复发。
//
// 为什么不在本包直接写库：model 依赖 loadbalancer（model/channel_candidates.go
// 用它做选路），loadbalancer 反向 import model 会构成循环。所以这里只声明
// 接口与注册入口，具体落库由 model 包实现并在启动时注册。
//
// 为什么不用 yaml：yaml 里的是**人工**规则（按渠道），学到的规则是按
// (渠道, 模型) 的观测结果，且会随上游能力变化失效。把它写进人工配置文件，
// 运维就得手工维护一份会自己过期的清单。

import (
	"errors"
	"sort"
	"sync"
)

// ErrParamStripStoreUnavailable 表示持久化后端尚未注册。
//
// 区别于「注册了但写失败」：未注册是启动顺序问题，通常会在几毫秒后自愈；
// 写失败则是库/表层面的真问题。调用方可以用它决定日志措辞，但两者的处理
// 一样——不阻塞请求、不把规则标成已落库，下次遇到再试。
var ErrParamStripStoreUnavailable = errors.New("param strip store not registered")

// ParamStripRule 是一条学到的裁剪规则：某渠道的某模型不支持某参数。
type ParamStripRule struct {
	ChannelID int
	Model     string
	Params    []string
}

// ParamStripStore 是 paramStripLearned 的持久化后端。
//
// 读侧在启动时把全部规则读回来；写侧在学到新规则时追加一条。
// 实现方必须自己保证幂等：同一条规则重复写入不应产生重复行。
//
// 只有读写两个方法被接口调用。渠道删除时的清理不经过这里：那条路径在 model
// 包（deleteParamStripRulesForChannel）同时清内存与库，且不依赖启动期的注册
// 顺序，接口里不必暴露一个从未被调用的 Delete 方法。
type ParamStripStore interface {
	// LoadParamStripRules 返回全部已持久化的规则。启动时调用一次。
	LoadParamStripRules() ([]ParamStripRule, error)
	// SaveParamStripRule 持久化一条规则。学到新参数时调用。
	SaveParamStripRule(rule ParamStripRule) error
}

var (
	paramStripStoreMu sync.RWMutex
	paramStripStore   ParamStripStore
)

// RegisterParamStripStore 注册持久化后端。
//
// 只能注册一次：重复注册意味着有两个后端在写同一份数据，重启后哪一份生效
// 取决于时序。第二次起直接忽略并返回 false。
func RegisterParamStripStore(store ParamStripStore) bool {
	if store == nil {
		return false
	}
	paramStripStoreMu.Lock()
	defer paramStripStoreMu.Unlock()
	if paramStripStore != nil {
		return false
	}
	paramStripStore = store
	return true
}

// GetParamStripStore 返回当前注册的持久化后端，未注册时返回 nil。
func GetParamStripStore() ParamStripStore {
	paramStripStoreMu.RLock()
	defer paramStripStoreMu.RUnlock()
	return paramStripStore
}

// LoadParamStripLearned 把持久化的规则读回内存。
//
// 启动时调用一次。读失败不阻塞启动：读不到就退回「全靠重新学」，也就是
// 改动前的行为，比启动失败好。
func LoadParamStripLearned() error {
	store := GetParamStripStore()
	if store == nil {
		return nil
	}
	rules, err := store.LoadParamStripRules()
	if err != nil {
		return err
	}
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	loaded := 0
	for _, rule := range rules {
		if rule.ChannelID <= 0 || rule.Model == "" {
			continue
		}
		key := paramStripKey{channelID: rule.ChannelID, model: rule.Model}
		set := paramStripLearned[key]
		if set == nil {
			set = make(map[string]struct{})
			paramStripLearned[key] = set
		}
		persisted := paramStripPersisted[key]
		if persisted == nil {
			persisted = make(map[string]struct{})
			paramStripPersisted[key] = persisted
		}
		for _, p := range rule.Params {
			if n := normalizeParamName(p); n != "" {
				set[n] = struct{}{}
				persisted[n] = struct{}{} // 从库里读回来的，天然算「已落库」
				loaded++
			}
		}
	}
	return nil
}

// persistLearnedRule 把一条新学到的规则写进持久化后端。
//
// 失败只返回错误，不影响本次请求：落库是「让下次重启少错一次」的优化，
// 不是请求正确性的前提。调用方记日志即可。
//
// 后端未注册时返回 ErrParamStripStoreUnavailable，而不是 nil：一旦在这里
// 谎报成功，MarkParamUnsupported 会把这条规则标成「已落库」，等 store 之后
// 注册好也不会再补写——这次学到的规则就永久丢了跨重启存活的机会。
func persistLearnedRule(channelID int, modelName string, param string) error {
	store := GetParamStripStore()
	if store == nil {
		return ErrParamStripStoreUnavailable
	}
	return store.SaveParamStripRule(ParamStripRule{
		ChannelID: channelID,
		Model:     modelName,
		Params:    []string{param},
	})
}

// ForgetParamStripLearnedForChannel 清掉某条渠道在内存里学到的全部规则。
//
// 渠道被删除时调用。渠道 id 会被复用，不清的话新渠道会继承上一条渠道的
// 参数限制 —— 那些规则当初是为完全不同的上游模型观测到的。
func ForgetParamStripLearnedForChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	for key := range paramStripLearned {
		if key.channelID == channelID {
			delete(paramStripLearned, key)
			delete(paramStripPersisted, key)
		}
	}
}

// ForgetParamStripLearnedRule 清掉**一条**学到的规则（渠道 × 模型 × 参数）。
//
// 供人工纠正误学规则用。学习路径只增不减（上游不会「重新支持」一个参数），
// 但误判确实存在：参数识别可能把一次 5xx 的散文式错误误判成参数问题，于是
// 该模型一个本应支持的参数被永久裁掉。这里让调用方去掉某一条具体规则，
// 而不必删整条渠道。
func ForgetParamStripLearnedRule(channelID int, modelName, param string) {
	param = normalizeParamName(param)
	if channelID <= 0 || param == "" {
		return
	}
	key := paramStripKey{channelID: channelID, model: modelName}
	paramStripMu.Lock()
	defer paramStripMu.Unlock()
	if set := paramStripLearned[key]; set != nil {
		delete(set, param)
		if len(set) == 0 {
			delete(paramStripLearned, key)
		}
	}
	if set := paramStripPersisted[key]; set != nil {
		delete(set, param)
		if len(set) == 0 {
			delete(paramStripPersisted, key)
		}
	}
}

// SnapshotParamStripLearned 返回当前内存里学到的全部规则，供持久化与排障使用。
// 结果按 (渠道, 模型, 参数) 排序，保证同一份状态每次导出都一样。
func SnapshotParamStripLearned() []ParamStripRule {
	paramStripMu.RLock()
	defer paramStripMu.RUnlock()
	out := make([]ParamStripRule, 0, len(paramStripLearned))
	for key, set := range paramStripLearned {
		if len(set) == 0 {
			continue
		}
		params := make([]string, 0, len(set))
		for p := range set {
			params = append(params, p)
		}
		sort.Strings(params)
		out = append(out, ParamStripRule{
			ChannelID: key.channelID,
			Model:     key.model,
			Params:    params,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChannelID != out[j].ChannelID {
			return out[i].ChannelID < out[j].ChannelID
		}
		return out[i].Model < out[j].Model
	})
	return out
}
