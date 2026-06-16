// Package api 提供Web前端页面的路由注册功能。
// 从嵌入式文件系统加载HTML模板和静态资源（CSS/JS/图片），
// 并注册所有前端页面的GET路由，实现SPA式管理后台。
package api

import (
	// html/template 提供HTML模板解析和执行功能。
	"html/template"
	// io/fs 提供文件系统接口，用于从嵌入式文件系统提取子目录。
	"io/fs"
	// net/http 提供HTTP状态码和文件服务功能。
	"net/http"

	// embedfiles 导入嵌入式文件系统包，包含Static和Templates目录。
	embedfiles "nattserver/Web/EmbedFiles"

	// github.com/gin-gonic/gin Gin Web框架。
	"github.com/gin-gonic/gin"
)

// registerFrontendRoutes 注册所有前端页面的路由和静态资源服务。
// 从嵌入式文件系统加载HTML模板，挂载静态资源目录，
// 并为每个前端页面注册对应的GET路由。
// 参数router：Gin路由引擎实例。
func registerFrontendRoutes(router *gin.Engine) {
	// 从嵌入式FS解析Templates目录下所有HTML模板文件
	// template.Must在解析失败时直接panic（模板错误应在启动时就暴露）
	tmpl := template.Must(template.New("").ParseFS(embedfiles.WebFs, "Templates/*.html"))
	// 将解析好的模板设置到Gin引擎
	router.SetHTMLTemplate(tmpl)

	// 从嵌入式文件系统中提取Static子目录
	staticFS, err := fs.Sub(embedfiles.WebFs, "Static")
	if err == nil {
		// 将Static目录挂载到/static路径，提供静态资源访问
		router.StaticFS("/static", http.FS(staticFS))
	}

	// 注册根路径和index.html路由（首页）
	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})
	router.GET("/index.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})
	// 登录页面路由
	router.GET("/login.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "login.html", nil)
	})
	// 用户协议页面路由
	router.GET("/agreement.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "agreement.html", nil)
	})
	router.GET("/resetpwd.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "resetpwd.html", nil)
	})
	// 批量注册管理后台的功能页面路由
	for _, page := range []struct {
		Path     string // URL路径
		Template string // 对应的HTML模板文件名
	}{
		{Path: "/dashboard.html", Template: "dashboard.html"}, // 仪表盘页面
		{Path: "/tunnels.html", Template: "tunnels.html"},     // 隧道管理页面
		{Path: "/config.html", Template: "config.html"},       // 系统配置页面
		{Path: "/mcp.html", Template: "mcp.html"},             // MCP管理页面
		{Path: "/audit.html", Template: "audit.html"},         // 审计日志页面
	} {
		// 在闭包中捕获循环变量p的副本，避免所有路由使用同一个变量值
		p := page
		// 注册该页面的GET路由
		router.GET(p.Path, func(c *gin.Context) {
			c.HTML(http.StatusOK, p.Template, nil)
		})
	}
}
