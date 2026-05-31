package ApiController

import (
	response "commonGin/Web/Services/CommonService"

	"github.com/gin-gonic/gin"
)

func Index(c *gin.Context) {
	c.JSON(response.JsonSuccessResponse("hello world"))
}
