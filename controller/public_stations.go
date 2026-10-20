package controller

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/model"
	"github.com/gin-gonic/gin"
)

// PublicStationsTag 是对外下发的渠道标签（PUBLIC_STATIONS_MODE=tagged 时生效）：
// 只有打上该标签的渠道才会出现在 /api/public_stations 里。mangoshare ai-hub
// 的渠道生成器 (generateNewApiChannelJson) 导入时统一打这个 tag。
const PublicStationsTag = "mangoshare-public"

// 公开模式（环境变量 PUBLIC_STATIONS_MODE）：
//   - "tagged"（默认）：仅下发打上 PublicStationsTag 的渠道；
//   - "all"：下发全部启用渠道（仍受投影白名单与内网地址过滤约束）；
//   - "off"：一律返回空列表。
func publicStationsMode() string {
	switch strings.TrimSpace(os.Getenv("PUBLIC_STATIONS_MODE")) {
	case "all":
		return "all"
	case "off":
		return "off"
	default:
		return "tagged"
	}
}

// isPubliclyPublishableBaseURL 过滤不适合对公网公开的上游地址：
// 回环、私网、链路本地地址以及空地址一律不下发 —— 本机侧车代理
// （127.0.0.1:xxxx 一类的内部网关渠道）不是「站点」，公开它们只会泄露
// 内部拓扑。该规则与公开模式无关，任何模式下都强制生效。
func isPubliclyPublishableBaseURL(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
	// 主机名形式：把 .local / 裸主机名（无点）视作内网。
	return strings.Contains(host, ".") && !strings.HasSuffix(host, ".local")
}

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
	if publicStationsMode() == "off" {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []publicStationItem{}})
		return
	}

	query := model.DB.Omit("key")
	if publicStationsMode() == "tagged" {
		query = query.Where("tag = ?", PublicStationsTag)
	}
	var channels []*model.Channel
	err := query.
		Where("status = ?", common.ChannelStatusEnabled).
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
		// 内网/回环上游在任何模式下都不公开。
		if !isPubliclyPublishableBaseURL(baseURL) {
			continue
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
