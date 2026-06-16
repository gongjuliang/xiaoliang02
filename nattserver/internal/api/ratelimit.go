// Package api 提供NATT服务端HTTP API层的速率限制（频控）功能。
// 实现基于键（如IP地址）的失败计数和阶梯封禁机制，
// 用于保护登录接口等敏感端点免受暴力破解攻击。
package api

import (
	// fmt 提供格式化字符串输出。
	"fmt"
	// sync 提供Mutex互斥锁，保证并发安全。
	"sync"
	// time 提供时间获取、比较和时间间隔计算。
	"time"
)

// RateLimiter 限流器结构体，使用同步Map记录每个键的失败状态。
// 线程安全，通过互斥锁保护并发访问。
type RateLimiter struct {
	// mu 互斥锁，保护failures map的并发读写。
	mu sync.Mutex
	// failures 键到限流状态的映射，键通常为IP地址或用户名。
	failures map[string]rateState
}

// rateState 单个键的限流状态，记录失败次数、封禁层级和封禁截止时间。
type rateState struct {
	// failures 当前累积的失败次数。
	failures int
	// level 封禁层级（0开始递增），决定封禁时长。
	level int
	// bannedTo 封禁截止时间，在此时间之前该键被禁止。
	bannedTo time.Time
}

// NewRateLimiter 创建一个新的限流器实例。
// 参数limit：单次窗口内允许的最大失败次数（当前硬编码为10次）。
// 参数window：失败计数的滑动窗口时长（当前未使用，保留供扩展）。
// 返回值：初始化好的RateLimiter指针。
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	// 初始化空的failures map
	return &RateLimiter{
		failures: make(map[string]rateState),
	}
}

// Allow 检查指定键是否被允许（即是否处于封禁状态）。
// 参数key：限流键（如IP地址）。
// 返回值：(剩余封禁时间, 是否允许)。允许时返回(0, true)，禁止时返回(剩余时间, false)。
func (r *RateLimiter) Allow(key string) (time.Duration, bool) {
	// 获取当前时间
	now := time.Now()
	// 加锁保护map访问
	r.mu.Lock()
	defer r.mu.Unlock()

	// 读取该键的限流状态
	state := r.failures[key]
	// 如果当前时间在封禁期内，返回剩余封禁时间和不允许
	if now.Before(state.bannedTo) {
		return time.Until(state.bannedTo), false // 返回距解封的剩余时间
	}
	// 未被封禁，允许
	return 0, true
}

// RecordFailure 记录指定键的一次失败（如密码错误）。
// 累积失败次数达到阈值（10次）后自动递增封禁层级并计算封禁时长。
// 参数key：限流键。
func (r *RateLimiter) RecordFailure(key string) {
	// 获取当前时间
	now := time.Now()
	// 加锁保护
	r.mu.Lock()
	defer r.mu.Unlock()

	// 读取当前状态
	state := r.failures[key]
	// 如果仍在封禁期内，不做额外处理
	if now.Before(state.bannedTo) {
		r.failures[key] = state
		return
	}
	// 封禁期已过，递增失败计数
	state.failures++
	// 失败次数达到10次，触发封禁
	if state.failures >= 10 {
		state.failures = 0                                 // 重置失败计数
		state.level++                                      // 递增封禁层级
		state.bannedTo = now.Add(banDuration(state.level)) // 根据层级计算封禁时长
	}
	// 更新状态回map
	r.failures[key] = state
}

// RecordSuccess 记录指定键的一次成功操作，重置该键的所有限流状态。
// 用于登录成功后清除之前累积的失败计数。
// 参数key：限流键。
func (r *RateLimiter) RecordSuccess(key string) {
	// 加锁保护
	r.mu.Lock()
	defer r.mu.Unlock()

	// 读取当前状态并重置
	state := r.failures[key]
	state.failures = 0           // 清除失败计数
	state.bannedTo = time.Time{} // 清除封禁截止时间（零值）
	// 更新状态
	r.failures[key] = state
}

// banDuration 根据封禁层级返回对应的封禁时长。
// 阶梯封禁策略：
//
//	层级 ≤1：5分钟
//	层级 =2：10分钟
//	层级 =3：30分钟
//	层级 =4：1小时
//	层级 ≥5：6小时
//
// 参数level：封禁层级。
// 返回值：封禁时长。
func banDuration(level int) time.Duration {
	switch {
	case level <= 1:
		return 5 * time.Minute // 第1级：5分钟
	case level == 2:
		return 10 * time.Minute // 第2级：10分钟
	case level == 3:
		return 30 * time.Minute // 第3级：30分钟
	case level == 4:
		return time.Hour // 第4级：1小时
	default:
		return 6 * time.Hour // 第5级及以上：6小时
	}
}

// formatBanDuration 将封禁时长格式化为人类可读的中文字符串。
// 参数duration：封禁时长。
// 返回值：格式化的中文字符串（如"5 分钟"、"2 小时"）。
func formatBanDuration(duration time.Duration) string {
	// 无效或非正时长，返回"稍后"
	if duration <= 0 {
		return "稍后"
	}
	// 四舍五入到分钟并计算总分钟数
	minutes := int(duration.Round(time.Minute) / time.Minute)
	// 不足1分钟按1分钟显示
	if minutes < 1 {
		minutes = 1
	}
	// 不足60分钟，以分钟显示
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	// 超过60分钟，以小时显示（向上取整）
	hours := minutes / 60
	if minutes%60 != 0 {
		hours++ // 有余数则向上取整
	}
	return fmt.Sprintf("%d 小时", hours)
}
