package Web

import (
	"commonGin/Web/EmbedFiles"
	"commonGin/Web/Routes"
	"fmt"
	"sync"

	"github.com/gin-gonic/gin"
)

func Start(ListenAddr string) {
	// 设置为发布模式，关闭调试信息
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	err := Routes.RegisterRoutes(router, EmbedFiles.WebFs)

	// 启动服务
	var wg sync.WaitGroup
	wg.Add(1)
	go func(router *gin.Engine) {
		defer wg.Done()
		err = router.Run(ListenAddr)
		if err != nil {
			fmt.Println("启动服务失败:", err)
		}
	}(router)

	wg.Wait() // 等待 goroutine 完成

}
