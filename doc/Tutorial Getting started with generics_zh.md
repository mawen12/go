# Tutorial Getting started with generics

## 内容

该教程介绍了Go中的泛型基础知识。借助泛型，你可以声明和使用 function 或 type。
这些 functions 或 types 是为与调用代码提供的一组类型中的任何类型一起使用而编写的。

在本教程中，你将声明两种简单的非泛型 function，然后在一个简单的泛型 function 中实现相同的逻辑。

您将完成一下部分：

    1. 创建一个目录用来保存代码
    2. 添加非泛型的 function
    3. 添加一个泛型的 function 来处理多种类型
    4. 当调用泛型 function 时，移除类型参数
    5. 声明类型约束

请注意：如果需要访问其他教程，[请访问](https://go.dev/doc/tutorial/)。

请注意：如果你愿意， 可以使用 ["Go dev branch" 模式的 Go Playground] 来编写和运行程序。

## 前提

- Go 1.18 或更高版本
- 编写代码的工具
- 命令行工具

## 创建一个目录用来保存代码

```bash
cd 

mkdir generics

cd generics

go mod init example/generics
```

请注意：对于生产环境的代码，你需要指定一个更适当的模块路径。要查看更多，确保使用 [Managing dependencies](https://go.dev/doc/modules/managing-dependencies)。

## 添加非泛型的 function

```go

```