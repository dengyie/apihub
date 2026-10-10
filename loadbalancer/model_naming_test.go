package loadbalancer

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dengyie/apihub/relaykit/types"
)

func streamBrokenErr() *types.NewAPIError {
	return &types.NewAPIError{StatusCode: http.StatusInternalServerError, Err: errors.New("stream error: INTERNAL_ERROR")}
}

func quotaErr() *types.NewAPIError {
	return &types.NewAPIError{StatusCode: http.StatusForbidden, Err: errors.New("insufficient_user_quota")}
}

// v29.12：熔断日志标签必须报「实际熔断范围」而不是错误分类。
//
// 生产日志里读到过假象：per_model 关闭时键已折叠回渠道级，日志却打
// 「渠道 #140 [deepseek-v4-flash] 已立即熔断」。排障的人会把结论带偏。

func TestEffectiveModelNameFallsBackToChannelScopeWhenPerModelOff(t *testing.T) {
	yes := true
	installPolicy(t, &Policy{
		Enabled: true,
		Default: ChannelPolicy{Breaker: BreakerPolicy{PerModel: &yes}},
	})
	// 开关关闭：策略里显式 false
	no := false
	installPolicy(t, &Policy{
		Enabled: true,
		Default: ChannelPolicy{Breaker: BreakerPolicy{PerModel: &no}},
	})
	err := streamBrokenErr()
	// 模型级错误，但键折叠成渠道级 → 标签必须说渠道级
	if got := EffectiveModelName(1, testModel, err); got != "" {
		t.Fatalf("开关关闭时应返回空串（渠道级），got %q", got)
	}
}

func TestEffectiveModelNameReportsModelWhenPerModelOn(t *testing.T) {
	installPolicy(t, perModelPolicy(2))
	err := streamBrokenErr()
	if got := EffectiveModelName(1, testModel, err); got != testModel {
		t.Fatalf("开关打开且模型级错误时应返回模型名，got %q", got)
	}
}

// v29.14 起不再有渠道级判据：账号级错误也按模型粒度熔断。
// 账号整体退出轮转由自动下线（AutomaticDisableChannelEnabled）负责，
// 熔断侧只负责「快速绕开」，不承担账号级语义。
func TestEffectiveModelNameAccountErrorAlsoModelScoped(t *testing.T) {
	installPolicy(t, perModelPolicy(2))
	err := quotaErr()
	if got := EffectiveModelName(1, testModel, err); got != testModel {
		t.Fatalf("v29.14 起额度耗尽也按模型粒度，got %q", got)
	}
}

// 边界：channelID<=0 与空模型名都退化到渠道级，不能凭空造出一把键。
func TestEffectiveModelNameDegenerateInputs(t *testing.T) {
	installPolicy(t, perModelPolicy(2))
	err := streamBrokenErr()
	for _, tc := range []struct {
		name      string
		channelID int
		model     string
	}{
		{"channelID=0", 0, testModel},
		{"channelID<0", -1, testModel},
		{"空模型名", 1, ""},
	} {
		if got := EffectiveModelName(tc.channelID, tc.model, err); got != "" {
			t.Fatalf("%s: 应返回空串，got %q", tc.name, got)
		}
	}
}

// v29.12：model_mapping 多对一时，映射到同一上游模型的兄弟客户端模型
// 要一起熔断，否则每个兄弟各自累计阈值，收敛慢一倍。

func TestSiblingModelsFromMapping(t *testing.T) {
	cases := []struct {
		name    string
		mapping string
		model   string
		want    []string
	}{
		{
			name:    "A、B 同映射到 X → 互为兄弟",
			mapping: `{"A":"X","B":"X","C":"Y"}`,
			model:   "A",
			want:    []string{"B"},
		},
		{
			name:    "兄弟结果稳定排序",
			mapping: `{"A":"X","B":"X","C":"X"}`,
			model:   "A",
			want:    []string{"B", "C"},
		},
		{
			name:    "不同上游不是兄弟",
			mapping: `{"A":"X","B":"Y"}`,
			model:   "A",
			want:    nil,
		},
		{
			name:    "自身不出现",
			mapping: `{"A":"X"}`,
			model:   "A",
			want:    nil,
		},
		{
			name:    "未映射模型按原样透传，B→A 算兄弟",
			mapping: `{"B":"A"}`,
			model:   "A",
			want:    []string{"B"},
		},
		{
			name:    "空映射无兄弟",
			mapping: `{}`,
			model:   "A",
			want:    nil,
		},
		{
			name:    "非法 JSON 不 panic",
			mapping: `not-json`,
			model:   "A",
			want:    nil,
		},
		{
			name:    "空模型名直接返回",
			mapping: `{"A":"X","B":"X"}`,
			model:   "",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SiblingModelsFromMapping(tc.mapping, tc.model)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// 端到端语义：熔 A 时兄弟 B 也必须退出轮转，而无关的 C 照常可用。
func TestSiblingTripRemovesSiblingButNotUnrelatedModel(t *testing.T) {
	installPolicy(t, perModelPolicy(4))
	tr := newTestTracker()
	siblings := SiblingModelsFromMapping(`{"A":"X","B":"X","C":"Y"}`, "A")
	for _, m := range append([]string{"A"}, siblings...) {
		tr.TripBreakerUntil(7, m, time.Now().Add(time.Minute))
	}
	if ok, _ := tr.IsAvailable(7, "A"); ok {
		t.Fatal("A 熔断后应不可用")
	}
	if ok, _ := tr.IsAvailable(7, "B"); ok {
		t.Fatal("兄弟 B 应随 A 一并熔断")
	}
	if ok, reason := tr.IsAvailable(7, "C"); !ok {
		t.Fatalf("无关模型 C 不该受影响，却报 %q", reason)
	}
}

// v29.12：「某模型持续 503」检测器。
//
// 判据是这个检测器唯一要守住的不变量：**成功选到渠道的模型永远不该告警**。

func exhaustionPolicy(threshold int) *Policy {
	return &Policy{
		Enabled: true,
		Default: ChannelPolicy{
			MaxInflight: 4,
			Breaker: BreakerPolicy{
				FailureThreshold:                    3,
				CooldownSeconds:                     60,
				HalfOpenProbes:                      1,
				ModelExhaustionThreshold:            threshold,
				ModelExhaustionWindowSeconds:        300,
				ModelExhaustionAlertCooldownSeconds: 1800,
			},
		},
	}
}

func resetExhaustion(t *testing.T) {
	t.Helper()
	exhaustionRegistry.mu.Lock()
	exhaustionRegistry.m = nil
	exhaustionRegistry.mu.Unlock()
}

func exhaustionCount(model string) int {
	exhaustionRegistry.mu.Lock()
	defer exhaustionRegistry.mu.Unlock()
	if st := exhaustionRegistry.m[model]; st != nil {
		return st.count
	}
	return 0
}
