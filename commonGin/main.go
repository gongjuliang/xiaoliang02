package main

import (
	"commonGin/Common/Const/RunConst"
	"commonGin/Web"
	"strconv"
)

//TIP <p>To run your code, right-click the code and select <b>Run</b>.</p> <p>Alternatively, click
// the <icon src="AllIcons.Actions.Execute"/> icon in the gutter and select the <b>Run</b> menu item from here.</p>

func main() {

	//启动gin服务
	Web.Start(RunConst.Ip + ":" + strconv.Itoa(RunConst.Port))
}
