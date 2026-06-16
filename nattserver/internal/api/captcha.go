// Package api 提供简单算术验证码（CAPTCHA）的生成、图片渲染和校验功能。
// 生成1-9范围内的随机加法算术题，渲染为PNG点阵图片，
// 验证码有5分钟有效期，过期自动清理。线程安全，支持测试注入。
package api

import (
	// bytes 提供Buffer缓冲区，用于PNG图片的内存写入。
	"bytes"
	// crypto/rand 提供密码学安全随机数用于验证码ID和算术题数字。
	"crypto/rand"
	// encoding/hex 将随机字节转为十六进制验证码ID。
	"encoding/hex"
	// fmt 提供格式化字符串输出。
	"fmt"
	// image 提供图片基本类型（RGBA图像）。
	"image"
	// image/color 提供颜色定义。
	"image/color"
	// image/draw 提供图片绘制功能。
	"image/draw"
	// image/png 提供PNG图片编码。
	"image/png"
	// math/big 提供大整数类型，配合crypto/rand生成随机整数。
	"math/big"
	// strings 提供字符串修剪和比较功能。
	"strings"
	// sync 提供互斥锁和线程安全的Map。
	"sync"
	// time 提供时间获取和超时判断。
	"time"
)

// captchaTTL 验证码有效期，超过此时间未验证的验证码将被清理。
const captchaTTL = 5 * time.Minute

// captchaTestAnswers 测试用验证码答案存储，供测试代码提取答案绕过图片识别。
// sync.Map保证并发安全。
var captchaTestAnswers sync.Map

// CaptchaStore 验证码存储管理器，线程安全地管理验证码的创建、查询和清理。
type CaptchaStore struct {
	// mu 互斥锁，保护entries map的并发访问。
	mu sync.Mutex
	// entries 验证码ID到验证码条目的映射。
	entries map[string]captchaEntry
	// now 时间获取函数（可注入用于测试）。
	now func() time.Time
}

// captchaEntry 单个验证码条目，包含算术题描述、正确答案和过期时间。
type captchaEntry struct {
	// question 算术题问题字符串（如"3 + 5 = ?"）。
	question string
	// answer 正确答案（如"8"）。
	answer string
	// expiresAt 过期时间，超过此时间的验证码无效。
	expiresAt time.Time
}

// captchaChallenge 验证码挑战数据结构，作为创建验证码的API响应。
type captchaChallenge struct {
	// ID 验证码唯一标识，用于后续获取图片和验证答案。
	ID string `json:"captcha_id"`
	// ImageURL 验证码图片的访问URL路径。
	ImageURL string `json:"image_url"`
}

// NewCaptchaStore 创建并初始化一个新的验证码存储。
// 返回值：验证码存储指针。
func NewCaptchaStore() *CaptchaStore {
	return &CaptchaStore{
		entries: make(map[string]captchaEntry), // 初始化空条目映射
		now:     time.Now,                      // 使用真实时钟
	}
}

// Create 创建一个新的验证码挑战（随机算术题）。
// 生成1-9范围内的两个随机数组成加法题，生成唯一ID，
// 将题目存入内存并设置5分钟过期时间。
// 返回值：验证码挑战数据和可能的错误。
func (s *CaptchaStore) Create() (captchaChallenge, error) {
	// 生成左操作数（1-9的随机数）
	left, err := randomInt(1, 9)
	if err != nil {
		return captchaChallenge{}, err
	}
	// 生成右操作数（1-9的随机数）
	right, err := randomInt(1, 9)
	if err != nil {
		return captchaChallenge{}, err
	}
	// 生成唯一验证码ID（32位十六进制）
	id, err := randomID()
	if err != nil {
		return captchaChallenge{}, err
	}
	// 构建验证码挑战（不含ImageURL，由调用方拼接）
	challenge := captchaChallenge{
		ID: id,
	}
	// 构建算术题问题描述
	question := fmt.Sprintf("%d + %d = ?", left, right)

	// 加锁保护map操作
	s.mu.Lock()
	// 先清理过期的验证码条目
	s.cleanupLocked(s.now())
	// 将新验证码存入entries映射
	s.entries[id] = captchaEntry{
		question:  question,                      // 算术题题目
		answer:    fmt.Sprintf("%d", left+right), // 正确答案
		expiresAt: s.now().Add(captchaTTL),       // 5分钟后过期
	}
	s.mu.Unlock()
	// 额外存入测试答案Map，供测试场景提取
	captchaTestAnswers.Store(id, fmt.Sprintf("%d", left+right))
	return challenge, nil
}

// Image 根据验证码ID生成算术题的PNG图片数据。
// 参数id：验证码唯一标识。
// 返回值：PNG图片字节数据和可能的错误（验证码不存在或已过期时返回错误）。
func (s *CaptchaStore) Image(id string) ([]byte, error) {
	// 去除ID的首尾空白
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("captcha id is required")
	}
	// 加锁读取验证码条目
	s.mu.Lock()
	now := s.now()
	// 先清理过期条目
	s.cleanupLocked(now)
	// 读取验证码条目
	entry, ok := s.entries[id]
	s.mu.Unlock()
	// 条目不存在或已过期
	if !ok || !now.Before(entry.expiresAt) {
		return nil, fmt.Errorf("captcha not found or expired")
	}
	// 将算术题渲染为PNG点阵图片
	return renderCaptchaPNG(entry.question)
}

// Verify 校验用户提交的验证码答案是否正确。
// 校验成功后立即删除验证码条目（一次性使用），校验失败条目保留。
// 参数id：验证码ID。
// 参数code：用户提交的答案字符串。
// 返回值：校验通过返回true，否则返回false。
func (s *CaptchaStore) Verify(id string, code string) bool {
	// 去除首尾空白
	id = strings.TrimSpace(id)
	code = strings.TrimSpace(code)
	// ID或答案为空白直接拒绝
	if id == "" || code == "" {
		return false
	}

	// 加锁保护
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// 先清理过期条目
	s.cleanupLocked(now)
	// 读取验证码条目
	entry, ok := s.entries[id]
	// 无论成功与否都删除条目（一次性使用）
	delete(s.entries, id)
	// 同步删除测试答案
	captchaTestAnswers.Delete(id)
	// 校验：条目必须存在、未过期、答案匹配（忽略大小写）
	return ok && now.Before(entry.expiresAt) && strings.EqualFold(entry.answer, code)
}

// captchaAnswerForTest 测试辅助函数：根据验证码ID获取正确答案。
// 用于测试场景绕过图片识别直接获取答案。
// 参数id：验证码ID。
// 返回值：正确答案字符串。
func captchaAnswerForTest(id string) string {
	// 从测试答案Map中加载
	value, _ := captchaTestAnswers.Load(id)
	// 类型断言为string
	answer, _ := value.(string)
	return answer
}

// cleanupLocked 清理过期的验证码条目（调用者必须持有s.mu锁）。
// 遍历所有条目，删除过期时间早于now的条目。
// 参数now：当前时间。
func (s *CaptchaStore) cleanupLocked(now time.Time) {
	// 遍历所有条目
	for id, entry := range s.entries {
		// 如果当前时间不在过期时间之前（即已过期）
		if !now.Before(entry.expiresAt) {
			delete(s.entries, id) // 删除过期条目
		}
	}
}

// randomInt 在[min, max]区间内生成一个密码学安全的随机整数。
// 参数min：最小值（含）。
// 参数max：最大值（含）。
// 返回值：随机整数和可能的错误。
func randomInt(min int64, max int64) (int64, error) {
	// 使用crypto/rand生成[0, max-min]范围内的随机大整数
	n, err := rand.Int(rand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return 0, err
	}
	// 偏移到[min, max]范围
	return n.Int64() + min, nil
}

// randomID 生成一个32字符的十六进制随机ID（16字节）。
// 返回值：十六进制编码的ID字符串和可能的错误。
func randomID() (string, error) {
	// 创建16字节缓冲区
	var buf [16]byte
	// 使用密码学安全随机数填充
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	// 转为十六进制字符串
	return hex.EncodeToString(buf[:]), nil
}

// glyphs 字符点阵定义：每个字符由7行×3列的点阵组成。
// "1"表示该位置需要绘制像素，"0"表示不绘制。
// 支持数字0-9、加号+、等号=、问号?的点阵渲染。
var glyphs = map[rune][]string{
	'0': {"111", "101", "101", "101", "101", "101", "111"}, // 数字0的7×3点阵
	'1': {"010", "110", "010", "010", "010", "010", "111"}, // 数字1
	'2': {"111", "001", "001", "111", "100", "100", "111"}, // 数字2
	'3': {"111", "001", "001", "111", "001", "001", "111"}, // 数字3
	'4': {"101", "101", "101", "111", "001", "001", "001"}, // 数字4
	'5': {"111", "100", "100", "111", "001", "001", "111"}, // 数字5
	'6': {"111", "100", "100", "111", "101", "101", "111"}, // 数字6
	'7': {"111", "001", "001", "010", "010", "010", "010"}, // 数字7
	'8': {"111", "101", "101", "111", "101", "101", "111"}, // 数字8
	'9': {"111", "101", "101", "111", "001", "001", "111"}, // 数字9
	'+': {"000", "010", "010", "111", "010", "010", "000"}, // 加号
	'=': {"000", "000", "111", "000", "111", "000", "000"}, // 等号
	'?': {"111", "001", "001", "010", "010", "000", "010"}, // 问号
}

// renderCaptchaPNG 将算术题文本渲染为PNG点阵图片。
// 使用像素级点阵字符绘制，PNG编码后的图片尺寸约150×44像素。
// 参数text：算术题文本（如"3 + 5 = ?"）。
// 返回值：PNG图片字节数据和可能的错误。
func renderCaptchaPNG(text string) ([]byte, error) {
	// 像素缩放因子：每个逻辑像素映射为4×4实际像素
	const scale = 4
	// 创建150×44像素的RGBA图像
	img := image.NewRGBA(image.Rect(0, 0, 150, 44))
	// 用浅灰色填充背景（RGB: 248, 250, 252）
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 248, G: 250, B: 252, A: 255}}, image.Point{}, draw.Src)

	// 定义深灰色绘制颜色（RGB: 30, 41, 59）
	ink := color.RGBA{R: 30, G: 41, B: 59, A: 255}
	// 起始X坐标（左边距12像素）
	x := 12
	// 遍历文本中的每个字符
	for _, ch := range text {
		// 空格跳过8像素
		if ch == ' ' {
			x += 8
			continue
		}
		// 绘制该字符的点阵图像
		drawGlyph(img, ch, x, 8, scale, ink)
		// 移动到下一个字符的位置（间距18像素）
		x += 18
	}
	// 将图像编码为PNG格式写入内存缓冲区
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	// 返回PNG字节切片
	return out.Bytes(), nil
}

// drawGlyph 在指定位置绘制单个字符的点阵图像。
// 参数img：目标RGBA图像。
// 参数ch：要绘制的字符。
// 参数x, y：绘制起始坐标（逻辑像素）。
// 参数scale：缩放因子。
// 参数ink：绘制颜色。
func drawGlyph(img *image.RGBA, ch rune, x int, y int, scale int, ink color.Color) {
	// 从点阵定义中获取字符的7行数据
	rows, ok := glyphs[ch]
	if !ok {
		return // 未定义的字符跳过
	}
	// 遍历7行
	for row, pattern := range rows {
		// 遍历每行的3列
		for col, bit := range pattern {
			// 只绘制标记为"1"的像素
			if bit != '1' {
				continue
			}
			// 计算该像素在图像中的矩形区域（经过scale缩放）
			rect := image.Rect(x+col*scale, y+row*scale, x+(col+1)*scale, y+(row+1)*scale)
			// 用ink颜色填充该矩形区域
			draw.Draw(img, rect, &image.Uniform{C: ink}, image.Point{}, draw.Src)
		}
	}
}
