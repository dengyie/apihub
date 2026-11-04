package helper

import (
	"fmt"
	"strings"
	"sync"
	"time"

	rootcommon "github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/logger"
	relaycommon "github.com/dengyie/apihub/relay/common"
	"github.com/dengyie/apihub/relaykit/dto"
	"github.com/dengyie/apihub/setting/ratio_setting"
	hostreasoning "github.com/dengyie/apihub/setting/reasoning"
	"github.com/gin-gonic/gin"
)

// invalidMappingLogInterval 限制同一个渠道的配置告警频率。
//
// 脏 model_mapping 是**持久**配置错误，不是偶发失败：一旦入库，该渠道上的
// 每一个请求都会走到这条分支。逐请求打日志会在几秒内刷满日志文件，把真正
// 有用的信息挤掉，而排障只需要一条就够定位渠道。
const invalidMappingLogInterval = 5 * time.Minute

// invalidMappingLoggedAt 记录每个渠道上一次告警时间。只在配置损坏时读写，
// 正常渠道在热路径上完全不碰这张表。
var invalidMappingLoggedAt sync.Map // map[int]time.Time

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	// map model name
	//
	// 渠道配置损坏（model_mapping 不是合法 JSON 对象，或映射成环）时不能把
	// 这个配置错误升级成整次请求的 500：请求名本身就是它要求的模型名，只要
	// 跳过映射、继续走规范化重名即可。一个渠道的脏数据会不会被选中取决于真正
	// 的路由决策（分组/优先级/权重），被选中就把 500 原样甩回客户端，等于让
	// 管理端的一个错字全量影响线上。损伤按渠道隔离并留痕，写侧另有校验。
	modelMapping := strings.TrimSpace(c.GetString("model_mapping"))
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		if err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap); err != nil {
			warnInvalidModelMapping(c, info, "不是合法的 JSON 对象", err, modelMapping)
			modelMap = nil
		}
		if modelMap != nil {
			// 支持链式模型重定向，最终使用链尾的模型
			currentModel := info.OriginModelName
			visitedModels := map[string]bool{
				currentModel: true,
			}
			for {
				mappedModel, exists := modelMap[currentModel]
				baseModel := hostreasoning.BaseModelName(currentModel)
				if (!exists || mappedModel == "") && baseModel != currentModel {
					mappedModel, exists = modelMap[baseModel]
				}
				if !exists || mappedModel == "" {
					for k, v := range modelMap {
						if strings.EqualFold(k, currentModel) {
							mappedModel = v
							exists = true
							break
						}
					}
					if (!exists || mappedModel == "") && baseModel != currentModel {
						for k, v := range modelMap {
							if strings.EqualFold(k, baseModel) {
								mappedModel = v
								exists = true
								break
							}
						}
					}
					if !exists || mappedModel == "" {
						normalized := ratio_setting.RoutingMatchModelName(currentModel)
						if normalized != "" && normalized != currentModel {
							mappedModel, exists = modelMap[normalized]
							if !exists || mappedModel == "" {
								for k, v := range modelMap {
									if strings.EqualFold(k, normalized) {
										mappedModel = v
										exists = true
										break
									}
								}
							}
						}
					}
				}
				if exists && mappedModel != "" {
					// 模型重定向循环检测，避免无限循环
					if visitedModels[mappedModel] {
						if mappedModel == currentModel {
							if currentModel == info.OriginModelName {
								info.IsModelMapped = false
								return nil
							}

							info.IsModelMapped = true
							break
						}
						// 成环的映射同样按「没有配置重定向」处理：与其让这条
						// 渠道上的请求全部 500，不如按客户端请求名原样转发，
						// 把错误留给上游按模型不存在报，并留下可定位的告警。
						warnInvalidModelMapping(c, info, "存在循环映射", nil, modelMapping)
						info.IsModelMapped = false
						break
					}
					visitedModels[mappedModel] = true
					currentModel = mappedModel
					info.IsModelMapped = true
				} else {
					break
				}
			}
			if info.IsModelMapped {
				info.UpstreamModelName = currentModel
			}
		}
	}

	if !info.IsModelMapped && info.OriginModelName != "" {
		normalized := ratio_setting.RoutingMatchModelName(info.OriginModelName)
		if normalized != "" && normalized != info.OriginModelName {
			info.UpstreamModelName = normalized
			info.IsModelMapped = true
		}
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}

// warnInvalidModelMapping 按渠道节流地记录一条配置告警。
//
// 节流键是渠道 id 而不是渠道 id+模型名：一个渠道的 model_mapping 是整份配置，
// 坏就是整份坏，按模型拆键只会让告警数量乘上该渠道挂的模型数。
func warnInvalidModelMapping(c *gin.Context, info *relaycommon.RelayInfo, reason string, err error, raw string) {
	channelID := c.GetInt("channel_id")
	if channelID == 0 && info != nil && info.ChannelMeta != nil {
		channelID = info.ChannelMeta.ChannelId
	}

	now := time.Now()
	if last, ok := invalidMappingLoggedAt.Load(channelID); ok {
		if lastAt, isTime := last.(time.Time); isTime && now.Sub(lastAt) < invalidMappingLogInterval {
			return
		}
	}
	invalidMappingLoggedAt.Store(channelID, now)

	originModel := ""
	if info != nil {
		originModel = info.OriginModelName
	}
	detail := ""
	if err != nil {
		detail = fmt.Sprintf("（%v）", err)
	}
	logger.LogWarn(c, fmt.Sprintf(
		"渠道 #%d 的 model_mapping %s%s，已跳过模型重映射（请求模型 %s，请修正渠道配置）；原始值: %s",
		channelID, reason, detail, originModel, truncateForLog(raw),
	))
}

// truncateForLog 截断要打进日志的 model_mapping 原文：渠道里可能堆了几百条
// 映射，整段照抄会刷爆日志，而排障只需要看到开头那一截就能认出脏 JSON 的形状。
func truncateForLog(raw string) string {
	const maxLen = 512
	raw = strings.TrimSpace(raw)
	if len(raw) <= maxLen {
		return raw
	}
	return raw[:maxLen] + "...(truncated)"
}
