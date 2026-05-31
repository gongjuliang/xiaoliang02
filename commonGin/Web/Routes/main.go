package Routes

import (
	"commonGin/Web/Controllers/ApiController"
	"commonGin/Web/Middlewares/HeaderMiddleware"
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 嵌入文件只能为源码文件同级目录和子目录下的文件
//

func RegisterRoutes(r *gin.Engine, embedFs embed.FS) error {
	//一、配置静态文件路由
	//1, 加载模板文件
	tmpl := template.Must(template.New("").ParseFS(embedFs, "Templates/*.html"))
	r.SetHTMLTemplate(tmpl)
	//2, 加载静态文件
	fp, _ := fs.Sub(embedFs, "Static")
	r.StaticFS("/static", http.FS(fp))
	//二、配置全局中间件
	r.Use(HeaderMiddleware.Hearder())
	//三.404设置
	r.NoRoute(func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusNotFound, "<h1>404!不好意思，该路径不存在~</h1>")
	})

	//四、路由设置
	//1.主页路由组
	{
		r.GET("/", func(c *gin.Context) {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, "欢迎访问工具人小良Gin基础框架")
		})
	}

	//2.API 路由组
	api := r.Group("/api")
	{
		api.GET("/index", ApiController.Index)
	}

	return nil

}
