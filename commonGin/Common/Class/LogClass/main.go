package LogClass

import (
	"fmt"
	"os"
	"path/filepath"
)

type logMessage struct {
	fileName string
	info     string
}

var logChannel chan logMessage

func init() {
	logChannel = make(chan logMessage, 1000) // 缓冲通道，提高性能
	go logWriter()
}

func logWriter() {
	for msg := range logChannel {
		err := writeLogToFile(msg.fileName, msg.info)
		if err != nil {
			//return
		}
	}
}
func writeLogToFile(fileName string, info string) error {
	logDir := GetLogDir()

	//查看是否存在文件
	if _, err := os.Stat(logDir + "/" + fileName); os.IsNotExist(err) {
		//不存在则创建
		file, err := os.Create(logDir + "/" + fileName)
		if err != nil {
			fmt.Println("创建日志文件失败：", logDir+"/"+fileName, err)
			return err
		}
		file.Close()
	}
	// 修改这里：使用追加模式写入文件
	file, err := os.OpenFile(logDir+"/"+fileName, os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer file.Close()

	// 追加写入信息
	_, err = file.WriteString(info + "\n")
	return err

}

func GetLogDir() string {
	//获取当前exe位置
	exePath, _ := os.Executable()
	//获取当前exe文件夹
	dir := filepath.Dir(exePath)
	//查看是否存在Log文件夹
	if _, err := os.Stat(dir + "/Log"); os.IsNotExist(err) {
		//不存在则创建
		os.Mkdir(dir+"/Log", os.ModePerm)
	}
	return dir + "/Log"
}

func SetLogDir(fileName string, info string) error {
	// 发送日志消息到通道
	logChannel <- logMessage{
		fileName: fileName,
		info:     info,
	}
	return nil
}
