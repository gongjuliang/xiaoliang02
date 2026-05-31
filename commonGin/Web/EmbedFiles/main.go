package EmbedFiles

import "embed"

// 嵌入web文件

//go:embed Static/*/* Templates/*
var WebFs embed.FS
