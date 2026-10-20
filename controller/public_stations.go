package controller

import (
	"net/http"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/model"
	"github.com/gin-gonic/gin"
)

// PublicStationsTag 是对外下发的渠道标签白名单：只有打上该标签的渠道才会
// 出现在 /api/public_stations 里。mangoshare ai-hub 的渠道生成器
// (generateNewApiChannelJson) 导入时统一打这个 tag，私有付费渠道只要不
// 打标就永远不会外泄。
const PublicStationsTag = "mangoshare-public"

// publicStationItem 是渠道的对外投影。字段是显式白名单而不是复用
// model.Channel 再 Omit：未来 Channel 新增的任何字段（key、settings、
// other_info、balance……）默认都不会出现在这个接口里。
type publicStationItem struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	Type         int      `json:"type"`
	Status       int      `json:"status"`
	BaseURL      string   `json:"base_url"`
	Models       []string `json:"models"`
	Group        string   `json:"group"`
	Priority     int64    `json:"priority"`
	Weight       int      `json:"weight"`
	Tag          string   `json:"tag"`
	ResponseTime int      `json:"response_time"`
	TestTime     int64    `json:"test_time"`
}

// GetPublicStations 下发 ai-hub 动态站点拓扑：供 share.mangoqwq.com 的
// ai-hub 页面把「后台实际接入的公益站渠道」渲染成站点卡片，替代纯前端
// 写死的预设列表。只读、无鉴权（走 /api 组的公共限流）、投影白名单如上。
func GetPublicStations(c *gin.Context) {
	var channels []*model.Channel
	err := model.DB.Where("tag = ?", PublicStationsTag).
		Omit("key").
		Order("priority desc, id asc").
		Find(&channels).Error
	if err != nil {
		common.SysError("failed to get public stations: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取公开站点拓扑失败"})
		return
	}

	items := make([]publicStationItem, 0, len(channels))
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		baseURL := ""
		if ch.BaseURL != nil {
			baseURL = strings.TrimRight(*ch.BaseURL, "/")
		}
		items = append(items, publicStationItem{
			ID:           ch.Id,
			Name:         ch.Name,
			Type:         ch.Type,
			Status:       ch.Status,
			BaseURL:      baseURL,
			Models:       ch.GetModels(),
			Group:        ch.Group,
			Priority:     ch.GetPriority(),
			Weight:       ch.GetWeight(),
			Tag:          ch.GetTag(),
			ResponseTime: ch.ResponseTime,
			TestTime:     ch.TestTime,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}
