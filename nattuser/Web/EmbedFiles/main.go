// Package EmbedFiles 提供对Web前端静态资源的嵌入式文件系统访问。
// 使用Go 1.16+的embed功能将Static（静态资源）和Templates（HTML模板）目录
// 编译到二进制文件中，使程序可以脱离外部文件独立运行，实现单一二进制部署。
package EmbedFiles

// import "embed" 引入Go标准库embed包，用于编译时嵌入文件到二进制。
import "embed"

// 上述编译指令指示Go编译器在编译时将Static目录和Templates目录的所有文件
// 嵌入到二进制中。Static包含CSS/JS/图片等静态资源，Templates包含HTML模板文件。
// WebFs 是对外暴露的嵌入文件系统变量，HTTP服务器通过它从内存中直接读取前端资源。
//
//go:embed Static Templates
var WebFs embed.FS
