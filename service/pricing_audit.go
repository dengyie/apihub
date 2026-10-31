package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dengyie/apihub/common"
	"github.com/dengyie/apihub/logger"
	"github.com/dengyie/apihub/model"
	"github.com/dengyie/apihub/relaykit/dto"
)

// defaultPricingAuditInterval is how often the unpriced-model sweep runs. New
// models appear when a channel catalog is refreshed, which the upstream model
// update task does on its own schedule, so a few hours of lag is enough to
// catch a freshly-advertised model before it bills much traffic.
const defaultPricingAuditInterval = 6 * time.Hour

// PricingAuditResult is stored as the system task result so the run history
// shows which models were unpriced at that time.
type PricingAuditResult struct {
	Checked  int                   `json:"checked"`
	Unpriced []model.UnpricedModel `json:"unpriced"`
}

// pricingAuditHandler sweeps the routing table for models that have an enabled
// channel but no billing entry.
//
// The bug it guards against is silent: GetModelRatio returns the 37.5 self-use
// ratio for an unknown model instead of an error, so an unpriced model bills
// every request at $75/M with no failure to notice. Nothing in the request path
// can point at it -- only the configuration can -- so this walks abilities and
// reports the gap.
type pricingAuditHandler struct{}

func (pricingAuditHandler) Type() string { return model.SystemTaskTypePricingAudit }

func (pricingAuditHandler) Enabled() bool { return true }

func (pricingAuditHandler) Interval() time.Duration {
	minutes := common.GetEnvOrDefault("PRICING_AUDIT_INTERVAL_MINUTES", int(defaultPricingAuditInterval/time.Minute))
	if minutes < 1 {
		minutes = int(defaultPricingAuditInterval / time.Minute)
	}
	return time.Duration(minutes) * time.Minute
}

// NewPayload is nil: the sweep always reads current state, so there is nothing
// to snapshot at enqueue time.
func (pricingAuditHandler) NewPayload() any { return nil }

func (pricingAuditHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	result, err := RunPricingAuditOnce(ctx)
	if err != nil {
		if finishErr := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusFailed, nil, err.Error()); finishErr != nil {
			common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, finishErr))
		}
		return
	}
	if finishErr := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); finishErr != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, finishErr))
	}
}

func init() {
	RegisterSystemTaskHandler(pricingAuditHandler{})
}

// RunPricingAuditOnce checks every routable model for a billing entry, logs a
// warning per run that finds any, and notifies the root user with the list so
// the gap is visible without reading logs.
func RunPricingAuditOnce(ctx context.Context) (*PricingAuditResult, error) {
	unpriced, checked, err := model.GetUnpricedModels()
	if err != nil {
		return nil, err
	}

	result := &PricingAuditResult{Checked: checked, Unpriced: unpriced}
	if len(unpriced) == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("pricing audit: all %d routable models have a billing entry", checked))
		return result, nil
	}

	names := make([]string, 0, len(unpriced))
	for _, item := range unpriced {
		names = append(names, item.Model)
	}
	sort.Strings(names)
	logger.LogWarn(ctx, fmt.Sprintf(
		"pricing audit: %d of %d routable models have no billing entry and fall back to the 37.5 self-use ratio (= $75/M): %s",
		len(unpriced), checked, strings.Join(names, ", "),
	))

	NotifyRootUser(
		dto.NotifyTypeChannelUpdate+"_pricing_audit",
		fmt.Sprintf("%d 个模型未配置价格，正在按保底倍率计费", len(unpriced)),
		buildPricingAuditMessage(unpriced),
	)
	return result, nil
}

// buildPricingAuditMessage formats the notification body. It lists every model
// with its channel count so a model served by a single stale channel can be
// told apart from one carrying real traffic.
func buildPricingAuditMessage(unpriced []model.UnpricedModel) string {
	var b strings.Builder
	b.WriteString("以下模型在 abilities 中有启用渠道，但计费配置里没有任何价格/倍率/表达式条目。\n")
	b.WriteString("请求不会报错，而是按自用模式保底倍率 37.5 计费（默认设置下等于 $75/M）。\n\n")
	for _, item := range unpriced {
		fmt.Fprintf(&b, "- %s（%d 个渠道）\n", item.Model, item.Channels)
	}
	b.WriteString("\n处理方式：在「系统设置 → 分组与模型定价设置」中补上价格，或停用不再需要该模型的渠道。\n")
	return b.String()
}
