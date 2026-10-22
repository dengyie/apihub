package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/loadbalancer"

	"gorm.io/gorm/clause"
)

// ParamStripRule 是「学到的参数裁剪规则」在库里的形态。
//
// 一行 = 一个 (渠道, 模型, 参数)。这样写而不是「一行存一个 JSON 数组」，
// 是为了让「这个参数之前学过没有」成为一次主键冲突检查（OnConflict
// DoNothing），而不是读出来解析 JSON 再比对。
//
// 唯一主键是 (channel_id, model, param)：同一规则被反复学到时只会有一行，
// 保存路径因此是幂等的。
//
// Model / Param 必须显式写 varchar：不给类型的话，MySQL 会把它们映射成
// longtext，而 longtext 不能作为唯一索引的键（要么报「BLOB/TEXT 用于 key
// 需指定前缀长度」，要么唯一约束根本没建成）。SQLite 上 varchar 与 text
// 等价（驱动按 schema.String 一律落 text），显式写上不影响 SQLite。
type ParamStripRule struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	ChannelID int    `json:"channel_id" gorm:"index:idx_param_strip_rule,unique,priority:1;not null"`
	Model     string `json:"model" gorm:"type:varchar(255);index:idx_param_strip_rule,unique,priority:2;not null"`
	Param     string `json:"param" gorm:"type:varchar(255);index:idx_param_strip_rule,unique,priority:3;not null"`
	// CreatedAt 是第一次学到的时刻。规则只增不减（上游不会「重新支持」一个
	// 参数），所以这个时间戳就是这条观测的起点。
	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime"`
}

func (ParamStripRule) TableName() string {
	return "param_strip_rules"
}

// ParamStripStore 是 loadbalancer.ParamStripStore 的落库实现。
//
// 放在 model 包是因为 model 已经在依赖 loadbalancer，反向不行；而 loadbalancer
// 只有通过接口才能拿到落库能力（见 loadbalancer/paramstrip_store.go）。
type ParamStripStore struct{}

// LoadParamStripRules 读回全部已持久化的规则。
func (s *ParamStripStore) LoadParamStripRules() ([]loadbalancer.ParamStripRule, error) {
	var rows []ParamStripRule
	if err := DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	grouped := make(map[string][]string)
	order := make([]string, 0, len(rows))
	keys := make(map[string]struct{})
	for _, row := range rows {
		groupKey := fmt.Sprintf("%d\x00%s", row.ChannelID, row.Model)
		if _, ok := keys[groupKey]; !ok {
			keys[groupKey] = struct{}{}
			order = append(order, groupKey)
		}
		grouped[groupKey] = append(grouped[groupKey], row.Param)
	}
	out := make([]loadbalancer.ParamStripRule, 0, len(order))
	for _, groupKey := range order {
		parts := strings.SplitN(groupKey, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		var channelID int
		if _, err := fmt.Sscanf(parts[0], "%d", &channelID); err != nil {
			continue
		}
		out = append(out, loadbalancer.ParamStripRule{
			ChannelID: channelID,
			Model:     parts[1],
			Params:    grouped[groupKey],
		})
	}
	return out, nil
}

// SaveParamStripRule 持久化一条规则；已存在则不改动（幂等）。
func (s *ParamStripStore) SaveParamStripRule(rule loadbalancer.ParamStripRule) error {
	if rule.ChannelID <= 0 || rule.Model == "" {
		return errors.New("invalid param strip rule")
	}
	for _, param := range rule.Params {
		if param == "" {
			continue
		}
		row := ParamStripRule{
			ChannelID: rule.ChannelID,
			Model:     rule.Model,
			Param:     param,
		}
		// DoNothing：同一条规则重复写入不产生第二行，也不更新 created_at
		// —— 「第一次学到的时刻」是这个时刻，不是最后一次被触发的时刻。
		if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			if isMissingTableError(err) {
				// 表还不存在（迁移未完成 / 非主节点）：内存里的规则照常生效，
				// 只是这一条没能跨重启存活。不该为它每条规则刷一行日志。
				return nil
			}
			return err
		}
	}
	return nil
}

// deleteParamStripRulesForChannel 同时清掉内存与库里的规则。
//
// 渠道被删除时调用。内存必须一起清：留着的话，同一进程里渠道 id 被复用时
// 新渠道会立刻继承上一条渠道学到的参数限制 —— 那些规则当初是为完全不同的
// 上游模型观测到的，对新渠道多半不成立。
//
// 直接写库而不走已注册的 store：删渠道时表就在手边，而 store 可能因为启动
// 顺序问题尚未注册（那是启动期的事，不该让「删渠道留了脏规则」有条件成立）。
//
// 表还不存在时静默跳过：滚动升级窗口里新二进制先起、表还没建好是正常
// 状态，这时清不掉规则只是残留几行无害数据，而报错会让人以为删渠道失败了。
func deleteParamStripRulesForChannel(channelID int) error {
	if channelID <= 0 {
		return nil
	}
	loadbalancer.ForgetParamStripLearnedForChannel(channelID)
	err := DB.Where("channel_id = ?", channelID).Delete(&ParamStripRule{}).Error
	if err != nil && isMissingTableError(err) {
		return nil
	}
	return err
}

// DeleteParamStripRule 删掉某条渠道某模型的一条具体规则，并清掉内存态。
//
// 这是「人工纠正误学规则」的唯一出口。学习路径只增不减（上游不会「重新
// 支持」一个参数），但误判确实存在：参数识别把一次 5xx 的散文式错误误判成
// 参数问题，就会让该模型一个本应支持的参数被永久裁掉。删整条渠道代价太大
// （那会断供给），所以提供到 (渠道, 模型, 参数) 粒度的删除。
func DeleteParamStripRule(channelID int, model string, param string) error {
	if channelID <= 0 || model == "" || param == "" {
		return errors.New("invalid param strip rule")
	}
	loadbalancer.ForgetParamStripLearnedRule(channelID, model, param)
	err := DB.Where("channel_id = ? AND model = ? AND param = ?", channelID, model, param).
		Delete(&ParamStripRule{}).Error
	if err != nil && isMissingTableError(err) {
		return nil
	}
	return err
}

// isMissingTableError 判断错误是不是「表不存在」。
//
// 只认这一种具体错误，不吞其它失败：写不进库是真问题，静默只会让它更难查。
func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "doesn't exist")
}

// InitParamStripStore 注册落库后端并把已持久化的规则读回内存。
//
// 必须在 model.InitDB 之后调用：表要存在，读回来才有内容。
//
// 表还不存在不算失败：迁移失败时网关照样要能跑（只是规则学不到跨重启），
// 而这里返回错误会让 main 把「一条可选优化」升级成「启动告警」。
func InitParamStripStore() error {
	if !loadbalancer.RegisterParamStripStore(&ParamStripStore{}) {
		// 已经注册过了：说明重复初始化，直接读一次即可。
		return loadbalancer.LoadParamStripLearned()
	}
	if err := loadbalancer.LoadParamStripLearned(); err != nil {
		if isMissingTableError(err) {
			common.SysLog("param strip rules table is not available yet; learned rules will not survive a restart yet")
			return nil
		}
		return err
	}
	rules := loadbalancer.SnapshotParamStripLearned()
	total := 0
	for _, r := range rules {
		total += len(r.Params)
	}
	common.SysLog(fmt.Sprintf("loaded %d learned param strip rules across %d channel/model pairs", total, len(rules)))
	return nil
}
