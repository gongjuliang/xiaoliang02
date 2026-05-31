package CommonService

import (
	"net/http"
)

type Response struct {
	Status  bool        `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func JsonSuccessResponse(data interface{}) (int, Response) {
	return http.StatusOK, Response{
		Status:  true,
		Message: "success",
		Data:    data,
	}
}

func JsonErrorResponse(message string) (int, Response) {
	return http.StatusOK, Response{
		Status:  false,
		Message: message,
		Data:    nil,
	}
}
