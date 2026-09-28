package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/loadbalancer"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout = 30 * time.Second
)

func isImageRelay(info *relaycommon.RelayInfo, c *gin.Context) bool {
	if info != nil {
		if info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits {
			return true
		}
	}
	if c != nil && c.Request != nil && c.Request.URL != nil {
		if strings.Contains(c.Request.URL.Path, "/images/") {
			return true
		}
	}
	return false
}

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) error {

	if resp == nil || dataHandler == nil {
		return nil
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	// 智能负载：跟踪本次请求的渠道状态
	channelID := 0
	if info != nil {
		channelID = info.GetChannelID()
	}
	lbHandle := loadbalancer.GlobalTracker().Begin(channelID)
	lbPolicy := loadbalancer.GetPolicy().Resolve(channelID)
	var lbTTFTSlow atomic.Bool
	var lbFirstByteOnce sync.Once

	// 首字超时检测：超时未收到首字则中断上游连接
	var ttftTimer *time.Timer
	if loadbalancer.Enabled() && lbPolicy.TTFTTimeoutMs > 0 {
		ttftTimer = time.AfterFunc(time.Duration(lbPolicy.TTFTTimeoutMs)*time.Millisecond, func() {
			lbFirstByteOnce.Do(func() {
				// 仍未收到首字：标记为慢，中断连接触发流结束
				lbTTFTSlow.Store(true)
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout,
					&loadbalancer.TTFTTimeoutError{ChannelID: channelID, TimeoutMs: lbPolicy.TTFTTimeoutMs})
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
			})
		})
		defer ttftTimer.Stop()
	}
	markFirstByte := func() {
		lbFirstByteOnce.Do(func() {
			if ttftTimer != nil {
				ttftTimer.Stop()
			}
			lbHandle.MarkFirstByte()
		})
	}
	defer func() {
		// 流结束：上报跟踪。failed 取流状态的真实结果：
		// 有错误（scanner 错误/超时/panic）或异常结束或空流（正常结束但无有效内容）都算失败，
		// 计入硬熔断计数器。注意 controller 层对流错误不再重复调用 RecordFailure，
		// 避免双记。
		failed := info.StreamStatus.HasErrors() || (!info.StreamStatus.IsNormalEnd() && info.StreamStatus.EndReason != relaycommon.StreamEndReasonClientGone)
		if !failed && info.StreamStatus.IsNormalEnd() && !isImageRelay(info, c) && info.ReceivedContentBytes < 100 {
			failed = true // 空流视同失败
		}
		lbHandle.End(lbTTFTSlow.Load(), failed)
	}()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second

	var (
		stopChan    = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner     = NewStreamScanner(resp.Body)
		ticker      = time.NewTicker(streamingTimeout)
		pingTicker  *time.Ticker
		writeMutex  sync.Mutex     // Mutex to protect concurrent writes
		wg          sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce sync.Once
		stopOnce    sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// 智能负载：首字节交付信号。ping 必须等首个上游字节交付后才开始，
	// 否则 ping 的写入会提前提交 HTTP 响应，关闭透明重试窗口。
	firstByteDelivered := make(chan struct{})
	var firstByteDeliveredOnce sync.Once
	signalFirstByteDelivered := func() {
		firstByteDeliveredOnce.Do(func() {
			close(firstByteDelivered)
		})
	}

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, fmt.Sprintf("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 等待首字节交付后再启动 ping，避免提前提交响应关闭重试窗口
			select {
			case <-firstByteDelivered:
			case <-ctx.Done():
				return
			case <-stopChan:
				return
			}

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, "ping data error: "+err.Error())
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, "ping goroutine max duration reached")
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		sr := newStreamResult(info.StreamStatus)
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				dataHandler(data, sr)
				// 首个数据已交付客户端：关闭透明重试窗口，允许 ping 启动
				signalFirstByteDelivered()
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", data)

			// 智能负载：首个有效数据到达即标记首字
			markFirstByte()

			if len(data) < 6 {
				continue
			}
			if data[:5] != "data:" && data[:6] != "[DONE]" {
				continue
			}
			data = data[5:]
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++
				// 累计实际内容字节数，用于检测"有块无内容"的空流
				// （如 gemini 正常结束但 completion_tokens=0 的情况）
				info.ReceivedContentBytes += len(data)

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: "+err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
				info.StreamStatus.RecordError(err.Error())
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
		info.StreamStatus.RecordError("streaming timeout")
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}

	cleanup()
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}

	// 首字节前 TTFT 超时：返回可透明重试的错误（此时客户端尚未收到任何数据）
	if lbTTFTSlow.Load() {
		return &loadbalancer.TTFTTimeoutError{ChannelID: channelID, TimeoutMs: lbPolicy.TTFTTimeoutMs}
	}
	// 空流：上游正常结束但一个有效内容块都没发，视同渠道失败。
	// 此时客户端尚未收到任何数据，返回可透明重试的错误，触发换渠道重试，
	// 而不是把空 200 返回给客户端。
	// 两种空流：
	// 1. 零内容块（received=0）
	// 2. 有块无内容（received>0 但总字节数极小，如 gemini 正常结束但 completion_tokens=0）
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() &&
		(info.ReceivedResponseCount == 0 || (!isImageRelay(info, c) && info.ReceivedContentBytes < 100)) {
		logger.LogError(c, fmt.Sprintf("空流：渠道 #%d 正常结束但零有效内容（块=%d, 字节=%d），触发换渠道重试",
			channelID, info.ReceivedResponseCount, info.ReceivedContentBytes))
		return &loadbalancer.EmptyStreamError{ChannelID: channelID}
	}

	// 流中断：上游在传输中途断开（如 HTTP/2 INTERNAL_ERROR、connection reset 等）。
	// 无论断开发生在首字节前还是传输中途，均视同渠道失败并返回 StreamBrokenError，
	// 触发立即熔断渠道并在重试预算内切换至下一个健康渠道重试。
	// 客户端主动取消（ClientGone）除外。
	if (info.StreamStatus.HasErrors() || !info.StreamStatus.IsNormalEnd()) &&
		info.StreamStatus.EndReason != relaycommon.StreamEndReasonClientGone {
		endReason := info.StreamStatus.Summary()
		logger.LogError(c, fmt.Sprintf("流中断：渠道 #%d 传输异常中断（%s, 已收块=%d, 字节=%d），触发换渠道重试并熔断",
			channelID, endReason, info.ReceivedResponseCount, info.ReceivedContentBytes))
		return &loadbalancer.StreamBrokenError{ChannelID: channelID, Reason: endReason}
	}
	return nil
}

// ToNewAPIError 将 StreamScannerHandler 返回的错误转换为 *types.NewAPIError。
// TTFT 超时错误会被标记为可重试，触发 controller 层的换渠道重试。
// 由于超时发生在首字节之前，客户端尚未收到任何数据，重试对客户端透明。
func ToNewAPIError(err error) *types.NewAPIError {
	if err == nil {
		return nil
	}
	if ttftErr, ok := err.(*loadbalancer.TTFTTimeoutError); ok {
		return types.NewErrorWithStatusCode(
			ttftErr,
			types.ErrorCodeChannelResponseTimeExceeded,
			http.StatusGatewayTimeout,
		)
	}
	// 空流：上游返回了空内容，视同 502 渠道失败，触发换渠道重试并计入熔断。
	if emptyErr, ok := err.(*loadbalancer.EmptyStreamError); ok {
		return types.NewErrorWithStatusCode(
			emptyErr,
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
		)
	}
	// 流中断：上游在首字节前断开，视同 502 渠道失败，触发换渠道重试并计入熔断。
	if brokenErr, ok := err.(*loadbalancer.StreamBrokenError); ok {
		return types.NewErrorWithStatusCode(
			brokenErr,
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
		)
	}
	return types.NewError(err, types.ErrorCodeBadResponseBody)
}
