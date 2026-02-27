// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package slog provides structured logging,
in which log records include a message,
a severity level, and various other attributes
expressed as key-value pairs.

It defines a type, [Logger],
which provides several methods (such as [Logger.Info] and [Logger.Error])
for reporting events of interest.

Each Logger is associated with a [Handler].
A Logger output method creates a [Record] from the method arguments
and passes it to the Handler, which decides how to handle it.
There is a default Logger accessible through top-level functions
(such as [Info] and [Error]) that call the corresponding Logger methods.

A log record consists of a time, a level, a message, and a set of key-value
pairs, where the keys are strings and the values may be of any type.
As an example,

	slog.Info("hello", "count", 3)

creates a record containing the time of the call,
a level of Info, the message "hello", and a single
pair with key "count" and value 3.

The [Info] top-level function calls the [Logger.Info] method on the default Logger.
In addition to [Logger.Info], there are methods for Debug, Warn and Error levels.
Besides these convenience methods for common levels,
there is also a [Logger.Log] method which takes the level as an argument.
Each of these methods has a corresponding top-level function that uses the
default logger.

The default handler formats the log record's message, time, level, and attributes
as a string and passes it to the [log] package.

	2022/11/08 15:28:26 INFO hello count=3

For more control over the output format, create a logger with a different handler.
This statement uses [New] to create a new logger with a [TextHandler]
that writes structured records in text form to standard error:

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

[TextHandler] output is a sequence of key=value pairs, easily and unambiguously
parsed by machine. This statement:

	logger.Info("hello", "count", 3)

produces this output:

	time=2022-11-08T15:28:26.000-05:00 level=INFO msg=hello count=3

The package also provides [JSONHandler], whose output is line-delimited JSON:

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("hello", "count", 3)

produces this output:

	{"time":"2022-11-08T15:28:26.000000000-05:00","level":"INFO","msg":"hello","count":3}

Both [TextHandler] and [JSONHandler] can be configured with [HandlerOptions].
There are options for setting the minimum level (see Levels, below),
displaying the source file and line of the log call, and
modifying attributes before they are logged.

Setting a logger as the default with

	slog.SetDefault(logger)

will cause the top-level functions like [Info] to use it.
[SetDefault] also updates the default logger used by the [log] package,
so that existing applications that use [log.Printf] and related functions
will send log records to the logger's handler without needing to be rewritten.

Some attributes are common to many log calls.
For example, you may wish to include the URL or trace identifier of a server request
with all log events arising from the request.
Rather than repeat the attribute with every log call, you can use [Logger.With]
to construct a new Logger containing the attributes:

	logger2 := logger.With("url", r.URL)

The arguments to With are the same key-value pairs used in [Logger.Info].
The result is a new Logger with the same handler as the original, but additional
attributes that will appear in the output of every call.

# Levels

A [Level] is an integer representing the importance or severity of a log event.
The higher the level, the more severe the event.
This package defines constants for the most common levels,
but any int can be used as a level.

In an application, you may wish to log messages only at a certain level or greater.
One common configuration is to log messages at Info or higher levels,
suppressing debug logging until it is needed.
The built-in handlers can be configured with the minimum level to output by
setting [HandlerOptions.Level].
The program's `main` function typically does this.
The default value is LevelInfo.

Setting the [HandlerOptions.Level] field to a [Level] value
fixes the handler's minimum level throughout its lifetime.
Setting it to a [LevelVar] allows the level to be varied dynamically.
A LevelVar holds a Level and is safe to read or write from multiple
goroutines.
To vary the level dynamically for an entire program, first initialize
a global LevelVar:

	var programLevel = new(slog.LevelVar) // Info by default

Then use the LevelVar to construct a handler, and make it the default:

	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(h))

Now the program can change its logging level with a single statement:

	programLevel.Set(slog.LevelDebug)

# Groups

Attributes can be collected into groups.
A group has a name that is used to qualify the names of its attributes.
How this qualification is displayed depends on the handler.
[TextHandler] separates the group and attribute names with a dot.
[JSONHandler] treats each group as a separate JSON object, with the group name as the key.

Use [Group] to create a Group attribute from a name and a list of key-value pairs:

	slog.Group("request",
	    "method", r.Method,
	    "url", r.URL)

TextHandler would display this group as

	request.method=GET request.url=http://example.com

JSONHandler would display it as

	"request":{"method":"GET","url":"http://example.com"}

Use [Logger.WithGroup] to qualify all of a Logger's output
with a group name. Calling WithGroup on a Logger results in a
new Logger with the same Handler as the original, but with all
its attributes qualified by the group name.

This can help prevent duplicate attribute keys in large systems,
where subsystems might use the same keys.
Pass each subsystem a different Logger with its own group name so that
potential duplicates are qualified:

	logger := slog.Default().With("id", systemID)
	parserLogger := logger.WithGroup("parser")
	parseInput(input, parserLogger)

When parseInput logs with parserLogger, its keys will be qualified with "parser",
so even if it uses the common key "id", the log line will have distinct keys.

# Contexts

Some handlers may wish to include information from the [context.Context] that is
available at the call site. One example of such information
is the identifier for the current span when tracing is enabled.

The [Logger.Log] and [Logger.LogAttrs] methods take a context as a first
argument, as do their corresponding top-level functions.

Although the convenience methods on Logger (Info and so on) and the
corresponding top-level functions do not take a context, the alternatives ending
in "Context" do. For example,

	slog.InfoContext(ctx, "message")

It is recommended to pass a context to an output method if one is available.

# Attrs and Values

An [Attr] is a key-value pair. The Logger output methods accept Attrs as well as
alternating keys and values. The statement

	slog.Info("hello", slog.Int("count", 3))

behaves the same as

	slog.Info("hello", "count", 3)

There are convenience constructors for [Attr] such as [Int], [String], and [Bool]
for common types, as well as the function [Any] for constructing Attrs of any
type.

The value part of an Attr is a type called [Value].
Like an [any], a Value can hold any Go value,
but it can represent typical values, including all numbers and strings,
without an allocation.

For the most efficient log output, use [Logger.LogAttrs].
It is similar to [Logger.Log] but accepts only Attrs, not alternating
keys and values; this allows it, too, to avoid allocation.

The call

	logger.LogAttrs(ctx, slog.LevelInfo, "hello", slog.Int("count", 3))

is the most efficient way to achieve the same output as

	slog.InfoContext(ctx, "hello", "count", 3)

# Customizing a type's logging behavior

If a type implements the [LogValuer] interface, the [Value] returned from its LogValue
method is used for logging. You can use this to control how values of the type
appear in logs. For example, you can redact secret information like passwords,
or gather a struct's fields in a Group. See the examples under [LogValuer] for
details.

A LogValue method may return a Value that itself implements [LogValuer]. The [Value.Resolve]
method handles these cases carefully, avoiding infinite loops and unbounded recursion.
Handler authors and others may wish to use [Value.Resolve] instead of calling LogValue directly.

# Wrapping output methods

The logger functions use reflection over the call stack to find the file name
and line number of the logging call within the application. This can produce
incorrect source information for functions that wrap slog. For instance, if you
define this function in file mylog.go:

	func Infof(logger *slog.Logger, format string, args ...any) {
	    logger.Info(fmt.Sprintf(format, args...))
	}

and you call it like this in main.go:

	Infof(slog.Default(), "hello, %s", "world")

then slog will report the source file as mylog.go, not main.go.

A correct implementation of Infof will obtain the source location
(pc) and pass it to NewRecord.
The Infof function in the package-level example called "wrapping"
demonstrates how to do this.

# Working with Records

Sometimes a Handler will need to modify a Record
before passing it on to another Handler or backend.
A Record contains a mixture of simple public fields (e.g. Time, Level, Message)
and hidden fields that refer to state (such as attributes) indirectly. This
means that modifying a simple copy of a Record (e.g. by calling
[Record.Add] or [Record.AddAttrs] to add attributes)
may have unexpected effects on the original.
Before modifying a Record, use [Record.Clone] to
create a copy that shares no state with the original,
or create a new Record with [NewRecord]
and build up its Attrs by traversing the old ones with [Record.Attrs].

# Performance considerations

If profiling your application demonstrates that logging is taking significant time,
the following suggestions may help.

If many log lines have a common attribute, use [Logger.With] to create a Logger with
that attribute. The built-in handlers will format that attribute only once, at the
call to [Logger.With]. The [Handler] interface is designed to allow that optimization,
and a well-written Handler should take advantage of it.

The arguments to a log call are always evaluated, even if the log event is discarded.
If possible, defer computation so that it happens only if the value is actually logged.
For example, consider the call

	slog.Info("starting request", "url", r.URL.String())  // may compute String unnecessarily

The URL.String method will be called even if the logger discards Info-level events.
Instead, pass the URL directly:

	slog.Info("starting request", "url", &r.URL) // calls URL.String only if needed

The built-in [TextHandler] will call its String method, but only
if the log event is enabled.
Avoiding the call to String also preserves the structure of the underlying value.
For example [JSONHandler] emits the components of the parsed URL as a JSON object.
If you want to avoid eagerly paying the cost of the String call
without causing the handler to potentially inspect the structure of the value,
wrap the value in a fmt.Stringer implementation that hides its Marshal methods.

You can also use the [LogValuer] interface to avoid unnecessary work in disabled log
calls. Say you need to log some expensive value:

	slog.Debug("frobbing", "value", computeExpensiveValue(arg))

Even if this line is disabled, computeExpensiveValue will be called.
To avoid that, define a type implementing LogValuer:

	type expensive struct { arg int }

	func (e expensive) LogValue() slog.Value {
	    return slog.AnyValue(computeExpensiveValue(e.arg))
	}

Then use a value of that type in log calls:

	slog.Debug("frobbing", "value", expensive{arg})

Now computeExpensiveValue will only be called when the line is enabled.

The built-in handlers acquire a lock before calling [io.Writer.Write]
to ensure that exactly one [Record] is written at a time in its entirety.
Although each log record has a timestamp,
the built-in handlers do not use that time to sort the written records.
User-defined handlers are responsible for their own locking and sorting.

# Writing a handler

For a guide to writing a custom handler, see https://golang.org/s/slog-handler-guide.
*/

// slog 包提供结构化的日志记录，其中日志记录包含一条消息，一个严重级别，
// 以及各种其他属性，这些属性以键值对的形式表达。
//
// 它定义了一个类型 [Logger]，提供了几个方法（如 [Logger.Info]、[Logger.Debug] 等）
// 用于报告感兴趣的事件。
//
// 每个 logger 都与一个 [Handler] 向关联。Logger 输出方法从方法参数创建一个 [Record],
// 并将其传递到 Handler，Handler 决定如何处理它。通过顶级函数（如 [Info]，[Error]）
// 访问默认的 Logger，这些函数调用对应的 Logger 方法。
//
// 日志记录由时间、级别、消息、和一组键值对组成。其中键是字符串，值可以是任何类型。例如，
//
//	slog.Info("hello", "count", 3)
//
// 创建一个包含调用时间、级别为 Info、消息 "hello" 和一个键为 "count" 值为 3 的记录。
//
// [Info] 顶级函数调用默认 Logger 的 [Logger.Info] 方法。除了 [Logger.Info] 外，
// 还有 Debug、Warn 和 Error 级别的方法。除了这些常用级别的便利方法外，还有一个 [Logger.Log] 方法。
// 该方法将级别作为参数。每个这些方法都有一个对应的顶级函数，使用默认 logger。
//
// 默认的 handler 将日志记录的消息、时间、级别和属性格式化为字符串，并将其传递到 [log] 包。
//
//	2022/11/08 15:28:26 INFO hello count=3
//
// 要更好的控制输出格式，可以创建一个具有不同 handler 的 logger。
// 以下语句使用 [New] 创建一个新的 logger，使用 [TextHandler] 将结构化记录以文本形式写入 stderr：
//
//	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
//
// [TextHandler] 输出是一系列 key-value 对，易于被机器解析。以下语句：
//
//	logger.Info("hello", "count", 3)
//
// 产生以下输出：
//
//	time=2022-11-08T15:28:26.000-05:00 level=INFO msg=hello count=3
//
// 包还提供了 [JSONHandler]，其输出是行分隔的，JSON:
//
//	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
//	logger.Info("hello", "count", 3)
//
// 产生以下输出：
//
//	{"time":"2022-11-08T15:28:26.000000000-05:00","level":"INFO","msg":"hello","count":3}
//
// [TextHandler] 和 [JSONHandler] 均可以使用 [HandlerOptions] 进行配置。
// 选项包括设置最小级别（查看 Levels），显示源文件和日志调用行号，并且在日志记录之前修改属性。
//
// 使用以下语句将 logger 设置为默认：
//
//	slog.SetDefault(logger)
//
// 这将导致像 [Info] 这样的顶级函数使用它。
// [SetDefault] 还更新了 [log] 包使用的默认 logger，
// 因此使用 [log.Printf] 和相关函数的现有应用程序无需重写就会将日志记录发送到 logger 的 handler。
//
// 一些属性对于许多日志调用是常见的。例如，你可能希望包含 URL 或跟踪服务器请求的标识符与请求相关
// 的所有日志事件。与其在每个日志调用中重复属性，不如使用 [Logger.With] 构造一个包含属性的新 Logger：
//
//	logger2 := logger.With("url", r.URL)
//
// With 的参数与 [Logger.Info] 中使用的键值对相同，结果是一个新的 Logger，具有与原始 Logger 相同的 handler，
// 但具有额外的属性，这些属性将出现在每个调用的输出中。
//
// # Levels
//
// [Level] 是一个整数，表示日志事件的重要性或严重程度。级别越高，事件越严重。
// 该包定义了最常见级别的常量，但任何 int 都可以用作级别。
//
// 在应用程序中，你可能希望仅在某个级别或更高级别记录消息。一种常见的配置是记录 Info 或更高级别的消息，
// 抑制 Debug 日志直到需要的时候才记录。
// 内置的 Handler 可以通过设置 [HandlerOptions.Level] 来配置输出的最小级别。程序的 `main` 函数通常会这样做。
// 默认值是 LevelInfo。
//
// 将 [HandlerOptions.Level] 字段设置为 [Level] 值会在其整个生命周期内固定 handler 的最小级别。
// 将其设置为 [LevelVar] 允许动态改变级别。LevelVar 持有一个 Level，并且可以安全地从多个 goroutine 读写。
// 要动态更改整个程序的级别，首先初始化一个全局的 LevelVar:
//
//	var programLevel = new(slog.LevelVar) // 默认为 Info
//
// 然后使用 LevelVar 构造一个 handler，并将其设置为默认：
//
//	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: programLevel})
//	slog.SetDefault(slog.New(h))
//
// 现在程序可以通过单个语句更改其日志级别：
//
//	programLevel.Set(slog.LevelDebug)
//
// # Groups
//
// 属性可以被收集到组中，组有一个名字，被用于限定其属性的名称。
// 这种限定的显示方式取决于 handler。[TextHandler] 用点分隔组和属性名称，
// [JSONHandler] 将每个组视为一个单独的 JSON 对象，组名作为键。
//
// 使用 [Group] 从名称和键值对列表创建一个 Group 属性：
//
//	  slog.Group("request",
//			"method", r.Method,
//			"url", r.URL)
//
// TextHandler 将此组显示为：
//
//	request.method=GET request.url=http://example.com
//
// JSONHandler 将其显示为：
//
//	"request":{"method":"GET","url":"http://example.com"}
//
// 使用 [Logger.WithGroup] 来限定 Logger 的所有输出具有一个组名。
// 调用 WithGroup 会得到一个新的 Logger，具有与原始 Logger 相同的 Handler,
// 但其所有属性都由组名限定。
//
// 这可以防止在大型系统中出现重复的属性键，其中子系统可能使用相同的键。
// 为每个子系统传递一个具有不同组名的 Logger，这样潜在的重复就会被限定。
//
//	logger := slog.Default().With("id", systemID)
//	parserLogger := logger.WithGroup("parser")
//	parserInput(input, parserLogger)
//
// 当使用 parserLogger 进行日志记录时，其键将被限定为 "parser"，
// 因此即使它使用了常见的键 "id"，日志行也会有不同的键。
//
// # Contexts
//
// 一些 handler 可能希望包含来自调用站点的 [context.Context] 的信息。
// 这类信息的一个例子是在启用跟踪时当前 span 的标识符。
//
// [Logger.Log] 和 [Logger.LogAttrs] 方法将 context 作为第一个参数，
// 其对应的低级函数也一样。例如：
//
//	slog.InfoContext(ctx, "message")
//
// 建议在可用时将 context 传递给输出方法。
//
// # Attrs and Values
//
// [Attr] 是一个键值对。Logger 输出方法接受 Attrs 以及交替的键和值。以下语句
//
//	slog.Info("hello", slog.Int("count", 3))
//
// 与以下行为相同：
//
//	slog.Info("hello", "count", 3)
//
// 常用类型有 [Int]、[String] 和 [Bool] 的便利构造函数，以及用于构造任何类型 Attr 的函数 [Any]。
//
// Attr 的值部分是一个类型叫做 [Value]。像 [any] 一样，Value 可以保存任何 Go 值。
// 但是它可以表示典型的值，包括所有数字和字符串，而无需分配。
//
// 为了高效地日志输出，使用 [Logger.LogAttrs]。它类似于 [Logger.Log]，但只接受 Attrs，
// 而不接受交替的键和值，这也允许它避免分配。
//
// 以下调用
//
//	logger.LogAttrs(ctx, slog.LevelInfo, "hello", slog.Int("count", 3))
//
// 是实现以下调用的最有效方式：
//
//	slog.InfoContext(ctx, "hello", "count", 3)
//
// # Customizing a type's logging behavior
//
// 如果一个类型实现了 [LogValuer] 接口，那么从其 LogValue 方法返回的 [Value] 将用于日志记录。
//
// 你可以使用它来控制类型的值在日志中的显示方式。例如，你可以隐藏密码等敏感信息，
// 或者在一个 Group 中收集一个 struct 的字段。有关详细信息，请参阅 [LogValuer] 下的示例。
//
// LogValue 方法可能返回一个 [Value]，它本身实现了 [LogValuer]。[Value.Resolve] 方法
// 会仔细处理这些情况，避免无限循环和无界递归。
// Handler 作者和其他人可能希望使用 [Value.Resolve] 而不是直接调用 LogValue。
//
// # Wrapping output methods
//
// logger 函数使用反射来查找应用程序中日志调用的文件名和行号。
// 这可能会为包装 slog 的函数产生不正确的源信息。例如：
// 如果你在 mylog.go 文件中定义了以下函数：
//
//	 func Infof(logger *slog.Logger, format string, args ...any) {
//			logger.Info(fmt.Sprintf(format, args...))
//	 }
//
// 并且你在 main.go 中这样调用它：
//
//	Infof(slog.Default(), "hello, %s", "world")
//
// 那么 slog 将报告源文件为 mylog.go，而不是 main.go。
//
// Infof 的正确实现将获取源位置（pc）并将其传递给 NewRecord。
// 包级示例中名为 "wrapping" 的 Infof 函数演示了如何做到这一点。
//
// # Working with Records
//
// 有时 Handler 需要在将 Record 传递给另一个 Handler 之前或之后进行处理。
// Record 包含了简单的公共字段（例如 Time, Level, Message）和间接引用
// 状态的字段（例如属性）。这意味着修改 Record 的简单副本（例如通过调用
// [Record.Add] 或 [Record.AddAttrs] 来添加属性）可能会对源产生意外的影响。
// 在修改 Record 之前，使用 [Record.Clone] 来创建一个与原始 Record 没有共享状态
// 的副本，或者使用 [NewRecord] 创建一个新的 Record，并通过 [Record.Attrs] 遍历
// 旧的 Record 来构建它的 Attrs。
//
// # Performance considerations
//
// 如果分析应用程序表明日志记录占用了大量时间，以下建议可能会有所帮助。
//
// 如果许多日志行具有公共属性，请使用 [Logger.With] 创建一个具有该属性的 Logger。
// 内置的 handler 将只在调用 [Logger.With] 时格式化该属性一次。
// [Handler] 接口被设计为允许这种优化，编写良好的 Handler 应该利用它。
//
// 日志调用的参数总数被评估，即使日志事件被丢弃。如果可能的话，推迟计算，
// 以便只有在值实际被记录时才进行计算。例如，考虑以下调用：
//
//	slog.Info("starting request", "url", r.URL.String()) // 可能会不必要的计算 String
//
// URL.String 方法将被调用，即使 logger 丢弃了 Info 级别的事件。相反，直接传递 URL：
//
//	slog.Info("starting request', "url", &r.URL) // 只有在需要时才调用 URL.String
//
// 内置的 [TextHandler] 将调用其 String 方法，但仅当日志事件启用时才调用它。
// 避免调用 String 还保留了底层值的结构。例如 [JSONHandler] 将解析后的 URL 的组件作为
// JSON 对象发出。如果你想要避免不必要的 String 调用而又不想让 handler 可能检查值的结构，
// 可以将值包装在一个 fmt.Stringer 实现中，隐藏其 Marshal 方法。
//
// 你也可以使用 [LogValuer] 接口来避免在禁用的日志调用中进行不必要的工作，假设你需要记录
// 一些昂贵的值：
//
//	slog.Debug("frobbing", "value", computeExpensiveValue(arg))
//
// 即使折行被禁用，computeExpensiveValue 也会被调用。为了避免这种情况，定义一个实现 LogValuer 的类型：
//
//	 type expensive struct { arg int }
//
//	 func (e expensive) LogValue() slog.Value {
//			return slog.AnyValue(computeExpensiveValue(e.arg))
//	 }
//
// 然后在日志调用中使用该类型的值：
//
//	slog.Debug("frobbing", "value", expensive{arg})
//
// 现在 computeExpensiveValue 只有在该行启用时才会被调用。
//
// 内置的 handler 在调用 [io.Writer.Write] 之前获取锁，以确保一次完整地写入一个 [Record]。
// 尽管每个日志记录都有一个时间戳，但内置的 handler 不使用该时间戳来排序写入的记录。
// 用户定义的 handler 负责自己的索引和排序。
//
// # Writing a handler
//
// 有关编写自定义 handler 的指南，请参阅 https://golang.org/s/slog-handler-guide。
package slog
