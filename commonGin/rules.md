# commonGin 项目架构与 AI 开发规范

> **本文档是 AI 编程助手的上下文规则，在后续生成代码时必须严格遵循。**
> 项目名：commonGin ｜ 语言：Go 1.25.0 ｜ 框架：Gin v1.12.0 ｜ 运行模式：Release

---

## 一、目录结构与模块职责

```
commonGin/
├── main.go                  # 程序入口，拼接监听地址并调用 Web.Start()
├── Common/                  # 公共组件层（与 Web 无关的通用逻辑）
│   ├── Class/               #   公共类/结构体封装
│   │   └── LogClass/        #     日志类：异步日志写入（chan + goroutine）
│   ├── Const/               #   公共常量
│   │   └── RunConst/        #     运行时常量（Ip、Port）
│   ├── Func/                #   公共函数集
│   │   ├── AtomicFunc/      #     原子操作函数
│   │   └── FileFunc/        #     文件操作函数
│   └── Var/                 #   公共变量
│       └── AtomicVar/       #     原子变量声明
├── Web/                     # Web 服务核心层
│   ├── main.go              #   Web 启动入口：创建 gin.Engine，调用路由注册，启动 HTTP 服务
│   ├── Config/              #   配置文件目录（待扩展）
│   ├── Controllers/         #   控制器层：处理 HTTP 请求，调用 Service 层
│   │   └── ApiController/   #     API 控制器（每个子目录对应一个路由分组）
│   ├── Services/            #   服务层：业务逻辑封装
│   │   └── CommonService/   #     通用服务（统一响应格式等）
│   ├── Middlewares/         #   中间件层
│   │   └── HeaderMiddleware/#     请求头中间件
│   ├── Routes/              #   路由注册中心
│   │   └── main.go          #     RegisterRoutes() 函数
│   ├── EmbedFiles/          #   嵌入式静态资源（Go embed）
│   │   ├── Static/          #     静态文件（JS/CSS/图片等）
│   │   ├── Templates/       #     HTML 模板文件
│   │   └── main.go          #     embed.FS 变量声明
│   └── Model/               #   数据模型层（待扩展）
├── Out/                     # 输出/构建产物目录
└── Test/                    # 测试代码目录
```

### 1.1 Common 层职责说明

| 子目录 | 职责 | 命名约定 |
|--------|------|----------|
| `Class/` | 封装可复用的结构体/类，每个子目录为一个类包 | `Class/XxxClass/main.go` |
| `Const/` | 存放全局常量，每个子目录按功能分组 | `Const/XxxConst/main.go` |
| `Func/` | 存放无状态的公共工具函数，按功能分组 | `Func/XxxFunc/main.go` |
| `Var/` | 存放全局变量，按功能分组 | `Var/XxxVar/main.go` |

### 1.2 Web 层职责说明

| 子目录 | 职责 |
|--------|------|
| `Controllers/` | 接收 `*gin.Context`，参数校验，调用 Service，返回响应。每个子目录对应一个路由分组（如 `ApiController` 对应 `/api`） |
| `Services/` | 封装业务逻辑，返回数据给 Controller。Controller 通过 **import 别名** 导入 Service |
| `Middlewares/` | 每个中间件一个子目录，导出 `gin.HandlerFunc` |
| `Routes/` | 唯一的路由注册入口 `RegisterRoutes()`，负责模板加载、静态文件、中间件挂载、路由分组 |
| `EmbedFiles/` | 通过 `//go:embed` 嵌入静态资源和模板，对外暴露 `WebFs embed.FS` |
| `Config/` | 存放应用配置（数据库连接、第三方密钥等） |
| `Model/` | 数据模型定义（数据库表结构、DTO 等） |

---

## 二、核心机制解析

### 2.1 启动流程

```
第一步：main.go 拼接 "Ip:Port" 地址，调用 Web.Start()
第二步：Web.Start() 设置 gin.ReleaseMode，创建 gin.New() 引擎
第三步：调用 Routes.RegisterRoutes(router, EmbedFiles.WebFs) 注册所有路由
第四步：在 goroutine 中启动 router.Run(ListenAddr)，使用 sync.WaitGroup 阻塞主协程
```

### 2.2 路由注册机制（RegisterRoutes）

`Web/Routes/main.go` 中的 `RegisterRoutes(r *gin.Engine, embedFs embed.FS) error` 是唯一的路由注册函数，执行顺序如下：

```
第一步：加载 HTML 模板 —— template.Must(template.New("").ParseFS(embedFs, "Templates/*.html"))
第二步：挂载静态文件 —— fs.Sub(embedFs, "Static") → r.StaticFS("/static", http.FS(fp))
第三步：注册全局中间件 —— r.Use(HeaderMiddleware.Hearder())
第四步：配置 404 处理 —— r.NoRoute(...)
第五步：注册路由分组
  - 主页路由组：直接挂载在根路径 "/"
  - API 路由组：r.Group("/api") 下挂载各 Controller 函数
```

### 2.3 Embed 嵌入方案

- 声明位置：`Web/EmbedFiles/main.go`
- 嵌入指令：`//go:embed Static/*/* Templates/*`
- 导出变量：`var WebFs embed.FS`
- 消费方式：`RegisterRoutes` 接收 `embed.FS` 参数，分别解析 Templates 和 Static
- **约束**：embed 只能嵌入源码文件同级及子目录下的文件

### 2.4 统一响应格式

响应结构体定义在 `Web/Services/CommonService/response.go`：

```go
type Response struct {
    Status  bool        `json:"status"`
    Message string      `json:"message"`
    Data    interface{} `json:"data"`
}
```

提供两个标准函数：

| 函数 | 用途 | 返回值 |
|------|------|--------|
| `JsonSuccessResponse(data interface{})` | 成功响应 | `(http.StatusOK, Response{true, "success", data})` |
| `JsonErrorResponse(message string)` | 失败响应 | `(http.StatusOK, Response{false, message, nil})` |

**Controller 调用方式**（通过 import 别名）：

```go
import response "commonGin/Web/Services/CommonService"

func Index(c *gin.Context) {
    c.JSON(response.JsonSuccessResponse("hello world"))
}
```

---

## 三、标准开发工作流（SOP）

### 3.1 新增一个 API 接口

```
第一步：在 Web/Controllers/ 对应控制器目录下新建 .go 文件（或修改已有文件）
  - 文件命名：小写蛇形，如 user_info.go
  - 函数命名：PascalCase，如 GetUserInfo
  - 必须接收 *gin.Context 参数
  - 通过 import 别名导入 CommonService 使用统一响应

第二步：在 Web/Routes/main.go 中注册路由
  - 找到对应的路由分组（如 api := r.Group("/api")）
  - 添加路由行：api.GET("/user/info", ApiController.GetUserInfo)
  - 如果控制器来自新的子目录，在文件顶部添加 import

第三步：如果控制器属于新的路由分组
  - 在 RegisterRoutes 函数中新建路由组：xxx := r.Group("/xxx")
  - 在 import 区添加新控制器的包路径
```

**示例 —— 新增 /api/user/info 接口：**

```go
// === 第一步：创建 Web/Controllers/ApiController/user_info.go ===
package ApiController

import (
    response "commonGin/Web/Services/CommonService"
    "github.com/gin-gonic/gin"
)

func GetUserInfo(c *gin.Context) {
    c.JSON(response.JsonSuccessResponse(gin.H{"name": "小良"}))
}

// === 第二步：修改 Web/Routes/main.go，在 api 路由组中添加 ===
api.GET("/user/info", ApiController.GetUserInfo)
```

### 3.2 新增一个 HTML 模板页面

```
第一步：在 Web/EmbedFiles/Templates/ 下创建 .html 文件
  - 文件名小写，如 about.html
  - embed 指令 "Templates/*" 会自动匹配，无需修改

第二步：在 Web/Controllers/ 对应控制器目录下创建渲染函数
  - 使用 c.HTML(http.StatusOK, "about.html", data) 渲染

第三步：在 Web/Routes/main.go 中注册对应路由
  - r.GET("/about", PageController.About)
```

### 3.3 新增一个静态资源文件

```
第一步：将文件放入 Web/EmbedFiles/Static/ 对应子目录
  - 如 Web/EmbedFiles/Static/css/style.css
  - embed 指令 "Static/*/*" 会自动匹配二级子目录

第二步：在 HTML 中通过 /static/ 路径引用
  - 如 <link rel="stylesheet" href="/static/css/style.css">
  - 无需修改路由，StaticFS 已自动映射
```

### 3.4 新增一个全局中间件

```
第一步：在 Web/Middlewares/ 下创建新的子目录
  - 命名：XxxMiddleware/，如 AuthMiddleware/
  - 在其中创建 main.go，导出返回 gin.HandlerFunc 的函数

第二步：在 Web/Routes/main.go 中挂载
  - 在"配置全局中间件"区域（第三步位置）添加：r.Use(XxxMiddleware.FuncName())
  - 在文件顶部 import 区添加中间件包路径
```

**示例 —— 新增认证中间件：**

```go
// === 第一步：创建 Web/Middlewares/AuthMiddleware/main.go ===
package AuthMiddleware

import "github.com/gin-gonic/gin"

func Auth() gin.HandlerFunc {
    return func(c *gin.Context) {
        // 认证逻辑
        c.Next()
    }
}

// === 第二步：修改 Web/Routes/main.go ===
// import 区添加：
//   "commonGin/Web/Middlewares/AuthMiddleware"
// 在"二、配置全局中间件"区域添加：
//   r.Use(AuthMiddleware.Auth())
```

### 3.5 新增一个路由分组

```
第一步：在 Web/Controllers/ 下创建新的控制器子目录
  - 命名：XxxController/，如 AdminController/

第二步：在 Web/Routes/main.go 中
  - import 新控制器包
  - 在"四、路由设置"区域新建路由组：
    admin := r.Group("/admin")
    {
        admin.GET("/list", AdminController.List)
    }
```

### 3.6 新增一个公共工具函数

```
第一步：判断是否可归入已有 Func 子目录，不可则创建新子目录
  - 命名：Common/Func/XxxFunc/main.go

第二步：编写函数，使用 PascalCase 命名，添加块注释（含参数和返回值说明）

第三步：在需要使用的地方 import "commonGin/Common/Func/XxxFunc"
```

### 3.7 新增一个 Service

```
第一步：在 Web/Services/ 下创建新的子目录
  - 命名：XxxService/，如 UserService/

第二步：编写业务逻辑函数

第三步：在 Controller 中通过 import 别名导入使用
  - import userService "commonGin/Web/Services/UserService"
```

---

## 四、命名与代码风格约定

### 4.1 目录与包命名

| 类型 | 规则 | 示例 |
|------|------|------|
| 目录名 | PascalCase，以功能+类型后缀命名 | `ApiController/`、`CommonService/`、`HeaderMiddleware/` |
| 包名 | 与目录名一致，PascalCase | `package ApiController` |
| Common 子目录 | `类型后缀` 命名：`XxxClass`、`XxxConst`、`XxxFunc`、`XxxVar` | `LogClass`、`RunConst`、`FileFunc`、`AtomicVar` |

### 4.2 文件命名

| 规则 | 示例 |
|------|------|
| 模块入口文件统一命名 `main.go` | `Web/main.go`、`Routes/main.go` |
| 控制器文件按功能小写蛇形命名 | `index.go`、`user_info.go` |
| Service 文件按功能小写蛇形命名 | `response.go`、`user_service.go` |

### 4.3 函数与变量命名

| 类型 | 规则 | 示例 |
|------|------|------|
| 导出函数（Controller/Service/中间件） | PascalCase | `Index()`、`GetUserInfo()`、`JsonSuccessResponse()` |
| 局部变量 | camelCase | `notExistPaths`、`logDir` |
| 常量 | PascalCase | `Ip`、`Port` |
| 私有函数 | camelCase | `writeLogToFile()`、`logWriter()` |

### 4.4 注释规范

1. **函数/结构体必须使用块注释**，包含参数说明和返回值说明：

```go
// CheckAllPathIsExist 批量检查多个文件路径是否全部存在
//
// 参数:
//   - paths: 需要检查的文件路径列表
//
// 返回值:
//   - bool: 所有路径都存在时返回 true，否则返回 false
//   - []string: 不存在的路径列表
func CheckAllPathIsExist(paths []string) (bool, []string) {
```

2. **重要代码块写总结注释**（如"第一步：加载模板文件"）
3. **简单代码行禁止写注释**
4. **禁止使用行尾注释**

### 4.5 Import 规范

1. Controller 导入 Service 时**必须使用 import 别名**：
   ```go
   import response "commonGin/Web/Services/CommonService"
   ```
2. import 分组顺序：标准库 → 项目内部包 → 第三方包
3. int 转 string 必须使用 `strconv.Itoa()`，禁止使用 `fmt.Sprintf()`

### 4.6 响应格式规范

所有 API 接口**必须**使用统一响应格式：
- 成功：`c.JSON(response.JsonSuccessResponse(data))`
- 失败：`c.JSON(response.JsonErrorResponse("错误信息"))`

---

## 五、架构约束与注意事项

1. **路由集中管理**：所有路由必须在 `Web/Routes/main.go` 的 `RegisterRoutes` 函数中注册，禁止在 Controller 中分散注册
2. **中间件统一挂载**：全局中间件在 `RegisterRoutes` 中"二、配置全局中间件"区域挂载
3. **embed 路径约束**：`//go:embed` 只能引用源码文件同级及子目录下的文件
4. **Release 模式**：项目始终以 `gin.ReleaseMode` 运行，禁止在代码中切换为 DebugMode
5. **服务启动方式**：使用 goroutine + `sync.WaitGroup` 启动 HTTP 服务，保持主协程阻塞
6. **Controller 单一职责**：Controller 只做请求处理和响应返回，业务逻辑必须放在 Service 层
7. **新增模块后**：必须同步更新 `Web/Routes/main.go` 中的 import 和路由注册
