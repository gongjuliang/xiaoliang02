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
