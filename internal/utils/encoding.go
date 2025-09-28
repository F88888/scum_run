package utils

import (
	"bytes"
	"fmt"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	_const "scum_run/internal/const"
	"strings"
	"unicode/utf8"
)

// EncodingType 编码类型枚举
type EncodingType int

const (
	EncodingUTF8 EncodingType = iota
	EncodingUTF16LE
	EncodingUTF16BE
	EncodingGBK
	EncodingGB2312
	EncodingUnknown
)

// String 返回编码类型的字符串表示
func (e EncodingType) String() string {
	switch e {
	case EncodingUTF8:
		return _const.EncodingUTF8
	case EncodingUTF16LE:
		return _const.EncodingUTF16LE
	case EncodingUTF16BE:
		return _const.EncodingUTF16BE
	case EncodingGBK:
		return _const.EncodingGBK
	case EncodingGB2312:
		return _const.EncodingGB2312
	default:
		return _const.EncodingUnknown
	}
}

// DetectStringEncoding 检测字符串的编码类型
// 支持检测 UTF-8、UTF-16LE、UTF-16BE 等常见编码格式
func DetectStringEncoding(input string) EncodingType {
	// 如果输入为空，返回UTF-8
	if len(input) == 0 {
		return EncodingUTF8
	}

	// 检查UTF-16LE BOM (0xFF 0xFE)
	if len(input) >= _const.UTF16BOMSize && input[0] == '\xFF' && input[1] == '\xFE' {
		return EncodingUTF16LE
	}

	// 检查UTF-16BE BOM (0xFE 0xFF)
	if len(input) >= _const.UTF16BOMSize && input[0] == '\xFE' && input[1] == '\xFF' {
		return EncodingUTF16BE
	}

	// 检查是否为有效的UTF-8
	if utf8.ValidString(input) {
		// 进一步检查是否包含UTF-16LE的特征（每两个字符间有null字符）
		if isUTF16LEString(input) {
			return EncodingUTF16LE
		}
		return EncodingUTF8
	}

	// 检查是否为UTF-16LE格式（偶数长度且包含null字符）
	if len(input)%_const.UTF16BytesPerChar == 0 && containsNullBytes(input) {
		return EncodingUTF16LE
	}

	return EncodingUnknown
}

// isUTF16LEString 检查字符串是否为UTF-16LE格式
// UTF-16LE字符串的特征：每两个字符间可能有null字符
func isUTF16LEString(input string) bool {
	if len(input) == 0 {
		return false
	}

	// 检查是否包含大量的null字符
	nullCount := 0
	for _, r := range input {
		if r == 0 {
			nullCount++
		}
	}

	// 如果null字符数量超过总字符数的1/3，可能是UTF-16LE
	return nullCount > len(input)/_const.UTF16MinNullRatio
}

// containsNullBytes 检查字符串是否包含null字节
func containsNullBytes(input string) bool {
	for _, r := range input {
		if r == 0 {
			return true
		}
	}
	return false
}

// ConvertToUTF8 将字符串转换为UTF-8编码
// 返回转换后的字符串、检测到的编码类型和可能的错误
func ConvertToUTF8(input string) (string, EncodingType, error) {
	if len(input) == 0 {
		return input, EncodingUTF8, nil
	}

	// 检测编码类型
	encoding := DetectStringEncoding(input)

	switch encoding {
	case EncodingUTF8:
		// 已经是UTF-8，直接返回
		return input, encoding, nil

	case EncodingUTF16LE:
		// 转换UTF-16LE到UTF-8
		utf8Str, err := convertUTF16LEToUTF8(input)
		if err != nil {
			return input, encoding, fmt.Errorf("UTF-16LE转换失败: %w", err)
		}
		return utf8Str, encoding, nil

	case EncodingUTF16BE:
		// 转换UTF-16BE到UTF-8
		utf8Str, err := convertUTF16BEToUTF8(input)
		if err != nil {
			return input, encoding, fmt.Errorf("UTF-16BE转换失败: %w", err)
		}
		return utf8Str, encoding, nil

	default:
		// 未知编码，尝试清理null字符后返回
		cleaned := CleanUTF16String(input)
		return cleaned, encoding, nil
	}
}

// convertUTF16LEToUTF8 将UTF-16LE字符串转换为UTF-8
func convertUTF16LEToUTF8(input string) (string, error) {
	// 移除BOM（如果存在）
	if len(input) >= _const.UTF16BOMSize && input[0] == '\xFF' && input[1] == '\xFE' {
		input = input[_const.UTF16BOMSize:]
	}

	// 确保字符串长度为偶数
	if len(input)%_const.UTF16BytesPerChar != 0 {
		input += "\x00"
	}

	// 将字符串转换为字节数组
	inputBytes := []byte(input)

	// 使用Go的UTF-16解码器
	decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
	reader := transform.NewReader(bytes.NewReader(inputBytes), decoder)

	var result bytes.Buffer
	_, err := result.ReadFrom(reader)
	if err != nil {
		return "", fmt.Errorf("UTF-16LE解码失败: %w", err)
	}

	return result.String(), nil
}

// convertUTF16BEToUTF8 将UTF-16BE字符串转换为UTF-8
func convertUTF16BEToUTF8(input string) (string, error) {
	// 移除BOM（如果存在）
	if len(input) >= _const.UTF16BOMSize && input[0] == '\xFE' && input[1] == '\xFF' {
		input = input[_const.UTF16BOMSize:]
	}

	// 确保字符串长度为偶数
	if len(input)%_const.UTF16BytesPerChar != 0 {
		input += "\x00"
	}

	// 将字符串转换为字节数组
	inputBytes := []byte(input)

	// 使用Go的UTF-16解码器
	decoder := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder()
	reader := transform.NewReader(bytes.NewReader(inputBytes), decoder)

	var result bytes.Buffer
	_, err := result.ReadFrom(reader)
	if err != nil {
		return "", fmt.Errorf("UTF-16BE解码失败: %w", err)
	}

	return result.String(), nil
}

// CleanUTF16String 清理UTF-16字符串中的null字符和控制字符
func CleanUTF16String(input string) string {
	if len(input) == 0 {
		return input
	}

	// 移除所有null字符
	cleaned := strings.ReplaceAll(input, "\x00", "")

	// 移除其他控制字符，保留可打印字符和常用空白字符
	var result strings.Builder
	for _, r := range cleaned {
		if r >= 32 || r == '\n' || r == '\r' || r == '\t' {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// IsValidUTF8 检查字符串是否为有效的UTF-8编码
func IsValidUTF8(input string) bool {
	return utf8.ValidString(input)
}

// TruncateString 截断字符串到指定长度，添加省略号
func TruncateString(input string, maxLength int) string {
	if len(input) <= maxLength {
		return input
	}
	return input[:maxLength] + _const.TruncateSuffix
}
