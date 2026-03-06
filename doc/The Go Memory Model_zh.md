# The Go Memory Model

## 介绍

Go memory model 规定了在什么条件下，一个 goroutine 中对一个变量的读取可以保证观察到另一个 goroutine 中对同一变量的写入所产生的值。

## 建议

修改多个 goroutine 同时访问的数据的程序必须对这种访问进行串行化。

为了实现访问序列化，请使用 `channel` 操作或其他同步原语（如 `sync` 和 `sync/atomic` 包中的原语）来保护数据。

如果您必须文档的以下部分来理解您程序的行为，那么您太明智了。

不要聪明。

## 非正式概述

Go 以与其他语言大致相同的方式来实现它的 memory model，目的是保持语义简洁，易懂且有用。
本节对该方法进行了总体概述，对于大多数程序员来说应该足够了。内存模式将在下一节中更正式
地进行阐述。

**Data Race** 是指对内存位置的写入操作与对同一位置的读取或写入操作同时发生，除非所有
涉及的访问都是 `sync/atomic` 包提供的原子数据访问，这样才能避免 **Data Race**。如前所述，
强烈建议程序员使用适当的同步机制来避免数据竞争。如果没有 **Data Race**，Go 程序的行为就像
所有 goroutine 都被复用到单个处理器上一样。这种特性有时被称为 DRF-SC：无数据竞争程序以
顺序一致的方式执行。

虽然程序员应该编写避免 Data Race 的 Go 程序，但 Go 实现对 Data Race 的处理方式仍然有限。
实现唯一可能的反应是报告竞争并中止程序，否则每次读取单字大小或亚字大小的内存位置时，
都必须观察到该位置实际写入的值（可能是由并发执行的 goroutine 写入的）尚未被覆盖。这些实现
上的限制使得 Go 更像 Java 或 JavaScript，因为大多数竞争条件的结果数量有限，而不像 C 和 C++,
因为在 C 和 C++ 中，任何包含竞争条件的程序的含义都是完全未定义的，编译器可以执行任何操作。
Go 的方法旨在使出错的程序更改可靠且更容易调试，同时仍然坚持认为 **Data Race** 是错误的，
并且工具可以诊断和报告这些错误。

## 内存模型

以下 Go 内存模型的正式定义与 Hans-J.Boehm 和 Sarita V.Adve 在 PLDI 2008 上发表的论文 
《Foundations of the C++ Concurrency Memory Model》中提出的方法非常接近。**No Data Race**
程序的定义以及 **No Data Race** 程序的顺序一致性保证与该工作中的定义和保证是等价的。

**Memory Model** 描述了程序执行的要求，程序执行由 goroutine 执行组成，而 goroutine 执行
又由内存操作组成。

**Memory Operation** 由四个细节构成模型：

- **Kind**，表明它是普通的数据读取、普通的数据写入，还是同步操作，例如原子数据访问、
互斥锁操作或 channel operation。
- **Location** 其在程序中的位置。
- 正在访问的内存位置或变量
- 被操作读/写的值

某些 **Memory Operation** 类似于读取操作，包含 read、atomic read、mutex lock 和 channel receive。
其他 **Memory Operation** 类似于写入操作，包含 write、atomic write、mutex unlock 和 channel send、channel close。
另外一些类似于读取+写入操作，例如 atomic compare-and-swap。

一个 **goroutine execution** 被建模为由单个 goroutine 执行的一组 **memory operations**。

**要求1**：每个 goroutine 中的 **memory operation** 必须与该 goroutine 的正确顺序执行相对应，
前提是已从内存读取和写入内存的值。

**要求2**：

**要求3**：







