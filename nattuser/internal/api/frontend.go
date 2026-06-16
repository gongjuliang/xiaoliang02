// Package api 提供Web前端页面的路由注册功能。
// 将嵌入的HTML模板文件（login/index/dashboard等）绑定到对应的HTTP路径，
// 并设置jQuery/Layui等静态资源的服务路径。
package api

import (
	"html/template"
	"io/fs"
	"net/http"

	embedfiles "nattuser/Web/EmbedFiles"

	"github.com/gin-gonic/gin"
)

func registerFrontendRoutes(router *gin.Engine) {
	tmpl := template.Must(template.New("").ParseFS(embedfiles.WebFs, "Templates/*.html"))
	router.SetHTMLTemplate(tmpl)

	staticFS, err := fs.Sub(embedfiles.WebFs, "Static")
	if err == nil {
		router.StaticFS("/static", http.FS(staticFS))
	}

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})
	router.GET("/index.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})
	router.GET("/login.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "login.html", nil)
	})
	router.GET("/agreement.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "agreement.html", nil)
	})
	router.GET("/resetpwd.html", func(c *gin.Context) {
		c.HTML(http.StatusOK, "resetpwd.html", nil)
	})
	for _, page := range []struct {
		Path     string
		Template string
	}{
		{Path: "/dashboard.html", Template: "dashboard.html"},
		{Path: "/tunnels.html", Template: "tunnels.html"},
		{Path: "/config.html", Template: "config.html"},
		{Path: "/mcp.html", Template: "mcp.html"},
		{Path: "/audit.html", Template: "audit.html"},
	} {
		p := page
		router.GET(p.Path, func(c *gin.Context) {
			c.HTML(http.StatusOK, p.Template, nil)
		})
	}
}
