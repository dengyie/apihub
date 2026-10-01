package loadbalancer

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
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

// 账号级错误即使开关打开也必须是渠道级：开关只决定粒度，不改判据。
func TestEffectiveModelNameChannelScopedErrorStaysChannelScoped(t *testing.T) {
	installPolicy(t, perModelPolicy(2))
	err := quotaErr()
	if got := EffectiveModelName(1, testModel, err); got != "" {
		t.Fatalf("账号级错误必须是渠道级，got %q", got)
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

func TestModelExhaustionCountsUpToThreshold(t *testing.T) {
	installPolicy(t, exhaustionPolicy(3))
	resetExhaustion(t)
	for i := 0; i < 2; i++ {
		RecordModelExhausted("m1", "group=default")
	}
	if got := exhaustionCount("m1"); got != 2 {
		t.Fatalf("阈值前应累计，got %d", got)
	}
	RecordModelExhausted("m1", "group=default") // 到阈值，触发后清零
	if got := exhaustionCount("m1"); got != 0 {
		t.Fatalf("触发告警后应清零以便下一轮重新计数，got %d", got)
	}
}

func TestModelExhaustionResetsOnSuccess(t *testing.T) {
	installPolicy(t, exhaustionPolicy(3))
	resetExhaustion(t)
	RecordModelExhausted("m1", "group=default")
	RecordModelExhausted("m1", "group=default")
	ResetModelExhausted("m1") // 模型仍在正常服务
	RecordModelExhausted("m1", "group=default")
	if got := exhaustionCount("m1"); got != 1 {
		t.Fatalf("成功后应从零重新累计，got %d", got)
	}
}

// 阈值配 0（未配置）走默认 5，而不是变成"永远不告警"或"立刻告警"。
func TestModelExhaustionNonPositiveThresholdFallsBackToDefault(t *testing.T) {
	installPolicy(t, exhaustionPolicy(0)) // 0 → OrDefault 给 5
	resetExhaustion(t)
	for i := 0; i < 4; i++ {
		RecordModelExhausted("m1", "g")
	}
	if got := exhaustionCount("m1"); got != 4 {
		t.Fatalf("未到默认阈值 5 应继续累计，got %d", got)
	}
	RecordModelExhausted("m1", "g") // 第 5 次到阈值 → 告警并清零
	if got := exhaustionCount("m1"); got != 0 {
		t.Fatalf("到默认阈值应触发并清零，got %d", got)
	}
	// 策略整体关闭时不记录
	installPolicy(t, &Policy{Enabled: false, Default: ChannelPolicy{Breaker: BreakerPolicy{ModelExhaustionThreshold: 1}}})
	RecordModelExhausted("m2", "g")
	if got := exhaustionCount("m2"); got != 0 {
		t.Fatalf("策略关闭时不应记录，got %d", got)
	}
}

func TestModelExhaustionIgnoresEmptyModelName(t *testing.T) {
	installPolicy(t, exhaustionPolicy(1))
	resetExhaustion(t)
	RecordModelExhausted("", "g")
	ResetModelExhausted("")
	exhaustionRegistry.mu.Lock()
	n := len(exhaustionRegistry.m)
	exhaustionRegistry.mu.Unlock()
	if n != 0 {
		t.Fatalf("空模型名不应建条目，got %d", n)
	}
}

// 模型名来自客户端请求，是无界输入：表必须能自我回收，否则是内存放大器。
func TestModelExhaustionRegistryStaysBounded(t *testing.T) {
	installPolicy(t, exhaustionPolicy(1))
	resetExhaustion(t)
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 2000; i++ {
		RecordModelExhausted("m", "g")
	}
	exhaustionRegistry.mu.Lock()
	for _, st := range exhaustionRegistry.m {
		st.windowStart = old
		st.lastAlert = old
	}
	exhaustionRegistry.mu.Unlock()
	RecordModelExhausted("trigger-sweep", "g") // 超过 1024 触发清扫

	exhaustionRegistry.mu.Lock()
	n := len(exhaustionRegistry.m)
	exhaustionRegistry.mu.Unlock()
	if n > 10 {
		t.Fatalf("清扫应把陈旧条目压到个位数，剩 %d", n)
	}
}
