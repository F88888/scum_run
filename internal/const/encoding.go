package _const

// 字符串编码相关常量
const (
	// 编码类型
	EncodingUTF8    = "UTF-8"
	EncodingUTF16LE = "UTF-16LE"
	EncodingUTF16BE = "UTF-16BE"
	EncodingGBK     = "GBK"
	EncodingGB2312  = "GB2312"
	EncodingUnknown = "Unknown"

	// 编码检测和转换相关常量
	MaxLogLineLength         = 1024 // 单条日志最大长度
	EncodingDetectionEnabled = true // 是否启用编码检测
	AutoConvertEncoding      = true // 是否自动转换编码

	// UTF-16相关常量
	UTF16BOMSize      = 2 // UTF-16 BOM大小
	UTF16MinNullRatio = 3 // UTF-16字符串中null字符的最小比例（1/3）
	UTF16BytesPerChar = 2 // UTF-16每个字符的字节数

	// 字符串处理相关常量
	MaxStringPreviewLength = 50    // 字符串预览最大长度
	TruncateSuffix         = "..." // 截断后缀
)
